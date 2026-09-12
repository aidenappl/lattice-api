package routers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aidenappl/lattice-api/automations"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/gorilla/mux"
)

// AutomationHandler serves automations. Plain CRUD calls queries directly, like
// every other handler; firing goes through the executor, which owns the run
// record, the concurrency guard and run-time authorisation.
type AutomationHandler struct {
	Executor *automations.Executor
}

// Error codes a webhook caller can branch on.
const (
	// errCodeAutomationSkipped — HTTP 409: the firing was recorded but
	// deliberately not run (already running, or no free runner slot).
	errCodeAutomationSkipped = 4091
	// errCodeAutomationFailed — HTTP 424: the run executed and a step failed.
	errCodeAutomationFailed = 4240
)

const maxAutomationNameLen = 128

// automationBody is the create/update request. On update every field is optional.
type automationBody struct {
	Name        *string                     `json:"name"`
	Description *string                     `json:"description"`
	Enabled     *bool                       `json:"enabled"`
	Trigger     *structs.AutomationTrigger  `json:"trigger"`
	Actions     *[]structs.AutomationAction `json:"actions"`
}

// automationWithToken carries a webhook secret the one time it is shown — at
// creation, at rotation, or when a trigger becomes a webhook. Only the hash is
// stored; there is no way to read the secret back.
type automationWithToken struct {
	*structs.Automation
	WebhookToken string `json:"webhook_token,omitempty"`
	WebhookPath  string `json:"webhook_path,omitempty"`
}

func withToken(a *structs.Automation, token string) automationWithToken {
	out := automationWithToken{Automation: a}
	if token != "" {
		out.WebhookToken = token
		out.WebhookPath = "/api/automations/" + token
	}
	return out
}

// HandleListAutomations lists automations, each with its latest run.
// GET /admin/automations
func (h *AutomationHandler) HandleListAutomations(w http.ResponseWriter, r *http.Request) {
	list, err := query.ListAutomations(db.DB, query.ListAutomationsRequest{Limit: db.MAX_LIMIT})
	if err != nil {
		responder.QueryError(w, err, "failed to list automations")
		return
	}

	ids := make([]int, len(list))
	for i, a := range list {
		ids[i] = a.ID
	}
	latest, err := query.ListLatestAutomationRuns(db.DB, ids)
	if err != nil {
		logger.Warn("automation", "failed to load latest runs", logger.F{"error": err})
	}

	viewer, _ := middleware.GetUserFromContext(r.Context())
	actors := map[int]*structs.AutomationActor{}
	for i := range list {
		if run, ok := latest[list[i].ID]; ok {
			list[i].LastRun = &run
		}
		decorateAutomation(&list[i], viewer, actors)
	}

	responder.New(w, list, "automations retrieved")
}

// HandleGetAutomation returns one automation with its latest run.
// GET /admin/automations/{id}
func (h *AutomationHandler) HandleGetAutomation(w http.ResponseWriter, r *http.Request) {
	a, ok := loadAutomation(w, r)
	if !ok {
		return
	}
	if latest, err := query.ListLatestAutomationRuns(db.DB, []int{a.ID}); err == nil {
		if run, ok := latest[a.ID]; ok {
			a.LastRun = &run
		}
	}
	viewer, _ := middleware.GetUserFromContext(r.Context())
	decorateAutomation(a, viewer, map[int]*structs.AutomationActor{})

	responder.New(w, a, "automation retrieved")
}

// HandleCreateAutomation creates an automation that runs as its creator.
// POST /admin/automations
func (h *AutomationHandler) HandleCreateAutomation(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body automationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}
	if body.Name == nil {
		responder.MissingBodyFields(w, "name")
		return
	}
	name, err := validateAutomationName(*body.Name)
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Trigger == nil {
		responder.MissingBodyFields(w, "trigger")
		return
	}
	if body.Actions == nil {
		responder.MissingBodyFields(w, "actions")
		return
	}
	normaliseTrigger(body.Trigger)

	if err := h.Executor.Validate(*body.Trigger, *body.Actions); err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid automation: "+err.Error())
		return
	}
	// The creator becomes the identity the automation runs as, so they must be
	// able to run every step of it themselves. See automations/authorize.go.
	if err := automations.Authorise(user, *body.Trigger, *body.Actions); err != nil {
		responder.SendError(w, http.StatusForbidden, "you cannot own this automation: "+err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	var token string
	var tokenHash *string
	if body.Trigger.Type == structs.AutomationTriggerWebhook {
		plaintext, hash, err := tools.GenerateToken()
		if err != nil {
			responder.SendError(w, http.StatusInternalServerError, "failed to generate webhook token", err)
			return
		}
		token, tokenHash = plaintext, &hash
	}

	a, err := query.CreateAutomation(db.DB, query.CreateAutomationRequest{
		Name:             name,
		Description:      cleanDescription(body.Description),
		Enabled:          enabled,
		Trigger:          *body.Trigger,
		Actions:          *body.Actions,
		WebhookTokenHash: tokenHash,
		RunAsUserID:      user.ID,
		CreatedBy:        user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to create automation")
		return
	}

	logAudit(r, "create", "automation", intPtr(a.ID),
		strPtr(fmt.Sprintf("%s (%s trigger, %d steps)", a.Name, a.Trigger.Type, len(a.Actions))))
	decorateAutomation(a, user, map[int]*structs.AutomationActor{})
	responder.NewCreated(w, withToken(a, token), "automation created")
}

// HandleUpdateAutomation edits an automation. Changing its trigger or actions —
// or enabling it — re-binds the run-as identity to the person saving, who must
// be able to run every step (see automations/authorize.go). Renaming does not.
// PUT /admin/automations/{id}
func (h *AutomationHandler) HandleUpdateAutomation(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}

	var body automationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		responder.BadBody(w, err)
		return
	}

	upd := query.UpdateAutomationRequest{}
	if body.Name != nil {
		name, err := validateAutomationName(*body.Name)
		if err != nil {
			responder.SendError(w, http.StatusBadRequest, err.Error())
			return
		}
		upd.Name = &name
	}
	if body.Description != nil {
		if d := cleanDescription(body.Description); d == nil {
			upd.ClearDescription = true
		} else {
			upd.Description = d
		}
	}

	trigger, actions := existing.Trigger, existing.Actions
	if body.Trigger != nil {
		normaliseTrigger(body.Trigger)
		trigger = *body.Trigger
	}
	if body.Actions != nil {
		actions = *body.Actions
	}
	redefined := body.Trigger != nil || body.Actions != nil
	enabling := body.Enabled != nil && *body.Enabled && !existing.Enabled

	if redefined || enabling {
		if err := h.Executor.Validate(trigger, actions); err != nil {
			responder.SendError(w, http.StatusBadRequest, "invalid automation: "+err.Error())
			return
		}
		if err := automations.Authorise(user, trigger, actions); err != nil {
			responder.SendError(w, http.StatusForbidden, "you cannot own this automation: "+err.Error())
			return
		}
		upd.RunAsUserID = &user.ID
	}
	if redefined {
		upd.Trigger = &trigger
		upd.Actions = &actions
	}
	if body.Enabled != nil {
		upd.Enabled = body.Enabled
	}

	var token string
	if trigger.Type != existing.Trigger.Type {
		if trigger.Type == structs.AutomationTriggerWebhook {
			plaintext, hash, err := tools.GenerateToken()
			if err != nil {
				responder.SendError(w, http.StatusInternalServerError, "failed to generate webhook token", err)
				return
			}
			token = plaintext
			upd.WebhookTokenHash = &hash
		} else {
			upd.ClearWebhookToken = true
		}
	}

	a, err := query.UpdateAutomation(db.DB, existing.ID, upd)
	if err != nil {
		responder.QueryError(w, err, "failed to update automation")
		return
	}

	details := a.Name
	if upd.RunAsUserID != nil {
		details += fmt.Sprintf(" (now runs as user #%d)", user.ID)
	}
	logAudit(r, "update", "automation", intPtr(a.ID), strPtr(details))
	decorateAutomation(a, user, map[int]*structs.AutomationActor{})
	responder.New(w, withToken(a, token), "automation updated")
}

// HandleDeleteAutomation retires an automation and kills its webhook token.
// DELETE /admin/automations/{id}
func (h *AutomationHandler) HandleDeleteAutomation(w http.ResponseWriter, r *http.Request) {
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}
	if err := query.DeleteAutomation(db.DB, existing.ID); err != nil {
		responder.QueryError(w, err, "failed to delete automation")
		return
	}
	logAudit(r, "delete", "automation", intPtr(existing.ID), strPtr(existing.Name))
	responder.New(w, nil, "automation deleted")
}

// HandleEnableAutomation switches an automation on and re-binds it to the person
// enabling it — whoever turns it on owns what it does.
// POST /admin/automations/{id}/enable
func (h *AutomationHandler) HandleEnableAutomation(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}

	// Re-validate: a container renamed or a stack deleted since the automation
	// was saved should fail here, loudly, not on the next firing.
	if err := h.Executor.Validate(existing.Trigger, existing.Actions); err != nil {
		responder.SendError(w, http.StatusBadRequest, "cannot enable: "+err.Error())
		return
	}
	if err := automations.Authorise(user, existing.Trigger, existing.Actions); err != nil {
		responder.SendError(w, http.StatusForbidden, "you cannot enable this automation: "+err.Error())
		return
	}

	enabled := true
	a, err := query.UpdateAutomation(db.DB, existing.ID, query.UpdateAutomationRequest{
		Enabled:     &enabled,
		RunAsUserID: &user.ID,
	})
	if err != nil {
		responder.QueryError(w, err, "failed to enable automation")
		return
	}

	logAudit(r, "enable", "automation", intPtr(a.ID), strPtr(fmt.Sprintf("%s (now runs as user #%d)", a.Name, user.ID)))
	decorateAutomation(a, user, map[int]*structs.AutomationActor{})
	responder.New(w, a, "automation enabled")
}

// HandleDisableAutomation switches an automation off. It does not re-bind the
// run-as identity: disabling only ever reduces what happens.
// POST /admin/automations/{id}/disable
func (h *AutomationHandler) HandleDisableAutomation(w http.ResponseWriter, r *http.Request) {
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}

	enabled := false
	a, err := query.UpdateAutomation(db.DB, existing.ID, query.UpdateAutomationRequest{Enabled: &enabled})
	if err != nil {
		responder.QueryError(w, err, "failed to disable automation")
		return
	}

	logAudit(r, "disable", "automation", intPtr(a.ID), strPtr(a.Name))
	viewer, _ := middleware.GetUserFromContext(r.Context())
	decorateAutomation(a, viewer, map[int]*structs.AutomationActor{})
	responder.New(w, a, "automation disabled")
}

// HandleRunAutomation fires an automation now, synchronously, and returns the
// run. The person pressing it must be able to do everything the run does; the
// run itself is still authorised against the run-as identity, so both checks
// apply.
// POST /admin/automations/{id}/run
func (h *AutomationHandler) HandleRunAutomation(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}
	if err := automations.Authorise(user, existing.Trigger, existing.Actions); err != nil {
		responder.SendError(w, http.StatusForbidden, "you cannot run this automation: "+err.Error())
		return
	}

	ip := r.RemoteAddr
	outcome, err := h.Executor.Fire(existing, automations.Firing{
		Source:      structs.AutomationSourceManual,
		Detail:      "manual run by " + user.Email,
		TriggeredBy: &user.ID,
		IP:          &ip,
	})
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to run automation", err)
		return
	}

	logAudit(r, "run", "automation", intPtr(existing.ID),
		strPtr(fmt.Sprintf("%s — run #%d %s", existing.Name, outcome.Run.ID, outcome.Result)))
	responder.New(w, outcome, fmt.Sprintf("automation run %s", outcome.Result))
}

// HandleRotateAutomationToken replaces a webhook automation's token. The old
// token stops working immediately; the new one is shown once.
// POST /admin/automations/{id}/rotate-token
func (h *AutomationHandler) HandleRotateAutomationToken(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r.Context())
	if !ok || user == nil {
		responder.SendError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}
	if existing.Trigger.Type != structs.AutomationTriggerWebhook {
		responder.SendError(w, http.StatusBadRequest, "only webhook automations have a token")
		return
	}
	// Minting its credential is owning it: the same check as saving it.
	if err := automations.Authorise(user, existing.Trigger, existing.Actions); err != nil {
		responder.SendError(w, http.StatusForbidden, "you cannot rotate this automation's token: "+err.Error())
		return
	}

	plaintext, hash, err := tools.GenerateToken()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to generate webhook token", err)
		return
	}
	a, err := query.UpdateAutomation(db.DB, existing.ID, query.UpdateAutomationRequest{WebhookTokenHash: &hash})
	if err != nil {
		responder.QueryError(w, err, "failed to rotate webhook token")
		return
	}

	logAudit(r, "rotate_token", "automation", intPtr(a.ID), strPtr(a.Name))
	decorateAutomation(a, user, map[int]*structs.AutomationActor{})
	responder.New(w, withToken(a, plaintext), "webhook token rotated; the previous token no longer works")
}

// HandleListAutomationRuns returns an automation's run history, newest first —
// including skipped firings and the reason for each.
// GET /admin/automations/{id}/runs?limit=&offset=
func (h *AutomationHandler) HandleListAutomationRuns(w http.ResponseWriter, r *http.Request) {
	existing, ok := loadAutomation(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	runs, err := query.ListAutomationRuns(db.DB, existing.ID, limit, offset)
	if err != nil {
		responder.QueryError(w, err, "failed to list automation runs")
		return
	}
	responder.New(w, runs, "automation runs retrieved")
}

// HandleAutomationWebhook fires the automation a webhook token belongs to.
// POST /api/automations/{token}
//
// Public: the token in the path is the only credential, exactly as for
// /api/deploy/{token}. It is CSRF-exempt and shares that endpoint's 1 rps limit.
//
// The run is synchronous so the caller learns the verdict, and the status is
// chosen so that a CI step turns red when — and only when — it should:
//
//	200  succeeded — every step ran and succeeded
//	200  disabled  — the automation is switched off; nothing ran, and the body says so
//	401            — the token resolves to no live automation; nothing is recorded
//	409  skipped   — already running, or no free runner slot; recorded, not queued
//	424  failed    — a step failed; the message names the run and the step
//
// 409 and 424 are deliberate. `curl --retry` and most CI HTTP clients retry 5xx
// and 429, and an automatically retried failure is the redeploy storm the
// concurrency guard exists to absorb. A failure must turn CI red without
// inviting a retry.
//
// The run executes on its own budget, never the request's context: a CI client
// that disconnects mid-run must not leave a half-applied automation behind.
func (h *AutomationHandler) HandleAutomationWebhook(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]
	ip := r.RemoteAddr
	detail := "webhook from " + ip
	if commit := r.URL.Query().Get("commit"); commit != "" {
		detail += " @ " + commit
	}

	outcome, err := h.Executor.FireWebhook(token, automations.Firing{
		Source: structs.AutomationSourceWebhook,
		Detail: detail,
		IP:     &ip,
	})
	if errors.Is(err, automations.ErrInvalidToken) {
		responder.SendError(w, http.StatusUnauthorized, "invalid automation token")
		return
	}
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, "failed to run automation", err)
		return
	}

	run := outcome.Run
	switch outcome.Result {
	case automations.ResultSucceeded:
		responder.New(w, outcome, fmt.Sprintf("automation run #%d succeeded", run.ID))
	case automations.ResultDisabled:
		responder.New(w, outcome, fmt.Sprintf("automation #%d is disabled; nothing was run (recorded as run #%d)", run.AutomationID, run.ID))
	case automations.ResultSkipped:
		reason := "skipped"
		if run.SkipReason != nil {
			reason = *run.SkipReason
		}
		responder.SendErrorWithCode(w, http.StatusConflict,
			fmt.Sprintf("automation run #%d was skipped: %s", run.ID, reason), errCodeAutomationSkipped)
	default:
		responder.SendErrorWithCode(w, http.StatusFailedDependency, describeFailedRun(run), errCodeAutomationFailed)
	}
}

// describeFailedRun names the run and the step that failed it, for a caller
// that sees only the error message.
func describeFailedRun(run *structs.AutomationRun) string {
	if run.FailedStep != nil && *run.FailedStep >= 1 && *run.FailedStep <= len(run.Steps) {
		step := run.Steps[*run.FailedStep-1]
		detail := step.Summary
		if step.Error != nil {
			detail = *step.Error
		}
		return fmt.Sprintf("automation run #%d failed at step %d (%s): %s", run.ID, step.Step, step.Type, detail)
	}
	if run.Error != nil {
		return fmt.Sprintf("automation run #%d failed: %s", run.ID, *run.Error)
	}
	return fmt.Sprintf("automation run #%d failed", run.ID)
}

func loadAutomation(w http.ResponseWriter, r *http.Request) (*structs.Automation, bool) {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		responder.SendError(w, http.StatusBadRequest, "invalid automation id")
		return nil, false
	}
	a, err := query.GetAutomationByID(db.DB, id)
	if err != nil {
		responder.QueryError(w, err, "failed to load automation")
		return nil, false
	}
	return a, true
}

// decorateAutomation attaches the run-as identity for display and redacts what
// a non-admin must not read. actors caches user lookups across a list.
func decorateAutomation(a *structs.Automation, viewer *structs.User, actors map[int]*structs.AutomationActor) {
	actor, cached := actors[a.RunAsUserID]
	if !cached {
		if u, err := query.GetUserByID(db.DB, a.RunAsUserID); err == nil && u != nil {
			actor = &structs.AutomationActor{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Active: u.Active}
		}
		actors[a.RunAsUserID] = actor
	}
	a.RunAs = actor

	if viewer == nil || viewer.Role != "admin" {
		a.Actions = automations.RedactActions(a.Actions)
	}
}

func validateAutomationName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if len(name) > maxAutomationNameLen {
		return "", fmt.Errorf("name must be at most %d characters", maxAutomationNameLen)
	}
	return name, nil
}

func cleanDescription(raw *string) *string {
	if raw == nil {
		return nil
	}
	d := strings.TrimSpace(*raw)
	if d == "" {
		return nil
	}
	return &d
}

// normaliseTrigger collapses whitespace in a cron expression so the stored form
// is canonical.
func normaliseTrigger(t *structs.AutomationTrigger) {
	t.Cron = strings.Join(strings.Fields(t.Cron), " ")
}
