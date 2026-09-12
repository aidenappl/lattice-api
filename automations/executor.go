// Package automations runs automations: named rules pairing one trigger with an
// ordered list of actions.
//
// This is the one place in the API with a service layer, and it is there
// because the domain is orchestration — a run spans several queries, worker
// dispatch, outbound HTTP, the audit log and a concurrency guard. Handlers still
// call queries directly for plain CRUD.
//
// Tight on purpose: one sequential loop and one switch (kindFor). There are no
// conditionals, no parallel steps, no retries, no templating between steps and
// no DSL. Each has a marked seam in AGENTS.md → Automations.
package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aidenappl/lattice-api/cron"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/aidenappl/lattice-api/webhooks"
)

const (
	// RUN_BUDGET bounds a whole run. It stays under the HTTP server's 60s
	// WriteTimeout (server.go) because a webhook caller waits for the verdict: a
	// run that outlived the connection would still finish, but CI would never
	// hear how.
	RUN_BUDGET = 50 * time.Second

	// CLAIM_STALE_AFTER is when an unreleased run guard may be broken, and when a
	// run still in progress is declared dead. Past the budget by a margin, so a
	// slow-but-alive run is never mistaken for a dead one.
	CLAIM_STALE_AFTER = RUN_BUDGET + time.Minute

	// MAX_CONCURRENT_RUNS bounds runner slots across every automation. A firing
	// that finds no free slot is recorded as skipped — never queued.
	MAX_CONCURRENT_RUNS = 10

	// MAX_ACTIONS bounds one automation's steps.
	MAX_ACTIONS = 20

	REDEPLOY_ACTION_TIMEOUT     = 10 * time.Second
	HTTP_ACTION_DEFAULT_TIMEOUT = 10 * time.Second
	HTTP_ACTION_MAX_TIMEOUT     = 30 * time.Second
	MAX_HTTP_BODY_BYTES         = 64 * 1024

	// Column widths in automation_runs.
	maxSkipReasonLen    = 255
	maxTriggerDetailLen = 255
)

// ErrInvalidToken means a webhook token resolved to no live automation. It is
// returned before anything is written: no touch, no run row, no claim.
var ErrInvalidToken = errors.New("invalid automation token")

// ErrSlotClaimed means a schedule slot was already claimed by another firing.
var ErrSlotClaimed = errors.New("schedule slot already claimed")

// ContainerRedeployer sends a recreate for one container to one worker.
// routers.ContainerActionHandler implements it, so an automation sends exactly
// the command the dashboard's Recreate button sends.
type ContainerRedeployer interface {
	RecreateContainer(container *structs.Container, workerID int) error
}

// Executor fires automations and records every firing as a run.
type Executor struct {
	store       Store
	redeployer  ContainerRedeployer
	doHTTP      func(ctx context.Context, r webhooks.Request) (*webhooks.Response, error)
	validateURL func(raw string) error

	// slots is the runner-slot semaphore. Acquired without blocking: a firing
	// that cannot get a slot is skipped with that reason, never queued.
	slots     chan struct{}
	runBudget time.Duration
	now       func() time.Time
}

func NewExecutor(store Store, redeployer ContainerRedeployer) *Executor {
	return &Executor{
		store:       store,
		redeployer:  redeployer,
		doHTTP:      webhooks.Deliver,
		validateURL: tools.ValidateExternalURL,
		slots:       make(chan struct{}, MAX_CONCURRENT_RUNS),
		runBudget:   RUN_BUDGET,
		now:         time.Now,
	}
}

// Firing describes what caused one run.
type Firing struct {
	Source structs.AutomationTriggerSource
	// Detail is a human-readable origin: the caller and commit for a webhook,
	// the slot for a schedule, the person for a manual run.
	Detail string
	// ScheduledAt is the nominal slot, schedule firings only. It is the claim key.
	ScheduledAt *time.Time
	// TriggeredBy is the person who pressed "run now", manual firings only.
	TriggeredBy *int
	// IP is the caller's address, recorded on every audit row the run writes.
	IP *string
}

// Result is a firing's verdict as a caller sees it. It differs from the run's
// status in one respect: a disabled automation's firing is a skipped run, but
// its result says "disabled" explicitly, because that is what a caller has to
// be told.
type Result string

const (
	ResultSucceeded Result = "succeeded"
	ResultFailed    Result = "failed"
	ResultSkipped   Result = "skipped"
	ResultDisabled  Result = "disabled"
)

// Outcome is what a firing produced.
type Outcome struct {
	Result Result                 `json:"result"`
	Run    *structs.AutomationRun `json:"run"`
}

// Validate reports why a definition cannot be saved, or nil.
func (e *Executor) Validate(trigger structs.AutomationTrigger, actions []structs.AutomationAction) error {
	switch trigger.Type {
	case structs.AutomationTriggerWebhook:
		if trigger.Cron != "" {
			return errors.New("a webhook trigger takes no cron expression")
		}
	case structs.AutomationTriggerSchedule:
		if err := cron.Validate(trigger.Cron); err != nil {
			return fmt.Errorf("schedule trigger: %w", err)
		}
	default:
		return fmt.Errorf("unknown trigger type %q", trigger.Type)
	}

	if len(actions) == 0 {
		return errors.New("an automation needs at least one action")
	}
	if len(actions) > MAX_ACTIONS {
		return fmt.Errorf("an automation may have at most %d actions, got %d", MAX_ACTIONS, len(actions))
	}
	for i, action := range actions {
		kind, err := kindFor(action.Type)
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		if err := kind.validate(e, action.Config); err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, action.Type, err)
		}
	}
	return nil
}

// RedactActions hides what a non-admin must not read: http_request header
// values and bodies. Each action type decides for itself via actionKind.redact;
// a type this build does not know is redacted entirely, since nothing here can
// say which of its fields are safe.
func RedactActions(actions []structs.AutomationAction) []structs.AutomationAction {
	out := make([]structs.AutomationAction, len(actions))
	for i, action := range actions {
		out[i] = action
		if kind, err := kindFor(action.Type); err == nil {
			out[i].Config = kind.redact(action.Config)
		} else {
			out[i].Config = json.RawMessage(`{"redacted":true}`)
		}
	}
	return out
}

// FireWebhook resolves a webhook token and fires its automation.
//
// An unknown, rotated or deleted token returns ErrInvalidToken having written
// NOTHING — not a touch, not a run row, not a claim. Only a token that resolves
// to a live automation stamps webhook_last_used_at.
func (e *Executor) FireWebhook(token string, f Firing) (*Outcome, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}
	a, err := e.store.GetAutomationByWebhookHash(tools.HashToken(token))
	if err != nil {
		if errors.Is(err, query.ErrNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if a == nil || a.Trigger.Type != structs.AutomationTriggerWebhook {
		return nil, ErrInvalidToken
	}

	if err := e.store.TouchWebhook(a.ID); err != nil {
		logger.Warn("automation", "failed to touch webhook last_used_at", logger.F{"automation_id": a.ID, "error": err})
	}
	return e.Fire(a, f)
}

// Fire runs an automation synchronously and returns its outcome. Used by the
// webhook and by "run now", whose callers wait for the verdict.
func (e *Executor) Fire(a *structs.Automation, f Firing) (*Outcome, error) {
	run, won, err := e.createRun(a, f)
	if err != nil {
		return nil, err
	}
	if !won {
		return nil, ErrSlotClaimed
	}

	if !a.Enabled {
		return e.skip(run, "automation is disabled; nothing was run", ResultDisabled), nil
	}
	if !e.tryAcquireSlot() {
		return e.skip(run, e.slotsBusyReason(), ResultSkipped), nil
	}
	defer e.releaseSlot()

	return e.execute(a, run, f), nil
}

// FireScheduled claims one schedule slot and, if this caller won it, runs the
// automation in the background. A slot already claimed is a no-op — that is how
// a slot fires once however many ticks see it.
//
// skipReason, when set, records the slot as skipped instead of running it (a
// slot older than the catch-up window). The returned channel closes when
// whatever was started has finished; the scheduler ignores it.
func (e *Executor) FireScheduled(a *structs.Automation, slot time.Time, skipReason string) <-chan struct{} {
	done := make(chan struct{})
	slot = slot.UTC()
	f := Firing{
		Source:      structs.AutomationSourceSchedule,
		Detail:      "slot " + slot.Format(time.RFC3339),
		ScheduledAt: &slot,
	}

	run, won, err := e.createRun(a, f)
	if err != nil {
		logger.Error("automation", "failed to claim schedule slot", logger.F{
			"automation_id": a.ID, "scheduled_at": slot, "error": err,
		})
		close(done)
		return done
	}
	if !won {
		close(done)
		return done
	}

	switch {
	case skipReason != "":
		e.skip(run, skipReason, ResultSkipped)
	case !a.Enabled:
		e.skip(run, "automation is disabled; nothing was run", ResultDisabled)
	case !e.tryAcquireSlot():
		e.skip(run, e.slotsBusyReason(), ResultSkipped)
	default:
		go func() {
			defer close(done)
			defer e.releaseSlot()
			e.execute(a, run, f)
		}()
		return done
	}
	close(done)
	return done
}

// FailStuckRuns closes out runs still in progress long after their budget — a
// control plane that died mid-run — and releases their run guards.
//
// Without this, a crash would leave a run reading "in progress" forever, which is
// indistinguishable from one that is genuinely still going.
func (e *Executor) FailStuckRuns() {
	runs, err := e.store.ListStuckRuns(e.now().UTC().Add(-CLAIM_STALE_AFTER))
	if err != nil {
		return
	}
	for _, run := range runs {
		msg := fmt.Sprintf("no outcome was recorded within %s of starting — the control plane most likely restarted mid-run. "+
			"Steps marked succeeded did take effect; the audit log has the rest", CLAIM_STALE_AFTER)
		steps := finishPending(run.Steps, 0, "not run: the run was interrupted")
		status := structs.AutomationRunFailed
		_ = e.store.UpdateRun(run.ID, query.UpdateAutomationRunRequest{
			Status: &status, Error: &msg, Steps: &steps, Finished: true,
		})
		_ = e.store.ReleaseAutomation(run.AutomationID, run.ID)
		logger.Warn("automation", "stuck automation run failed", logger.F{
			"automation_id": run.AutomationID, "run_id": run.ID,
		})
	}
}

// ── one run ─────────────────────────────────────────────────────────────────

func (e *Executor) createRun(a *structs.Automation, f Firing) (*structs.AutomationRun, bool, error) {
	runAs := a.RunAsUserID
	var detail *string
	if f.Detail != "" {
		d := truncate(f.Detail, maxTriggerDetailLen-3)
		detail = &d
	}
	return e.store.CreateRun(query.CreateAutomationRunRequest{
		AutomationID:  a.ID,
		TriggerSource: f.Source,
		TriggerDetail: detail,
		ScheduledAt:   f.ScheduledAt,
		Status:        structs.AutomationRunInProgress,
		RunAsUserID:   &runAs,
		TriggeredBy:   f.TriggeredBy,
		Steps:         pendingSteps(a.Actions),
		StartedAt:     e.now().UTC(),
	})
}

// execute is the one executor loop: authorise, then each step in order.
func (e *Executor) execute(a *structs.Automation, run *structs.AutomationRun, f Firing) (out *Outcome) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("internal error: %v", r)
			logger.Error("panic", msg, logger.F{"goroutine": "automation-run", "run_id": run.ID})
			out = e.finish(run, finishPending(run.Steps, 0, "not run: the run crashed"), nil, &msg)
		}
	}()

	// The concurrency guard. One automation never runs twice at once: an overlap
	// is recorded as skipped and NOT queued, because a queue is exactly how a
	// retried webhook turns into a redeploy storm.
	claimed, err := e.store.ClaimAutomation(a.ID, run.ID, e.now().UTC().Add(-CLAIM_STALE_AFTER))
	if err != nil {
		msg := "could not take the automation's run guard: " + err.Error()
		return e.finish(run, finishPending(run.Steps, 0, "not run: the run guard could not be taken"), nil, &msg)
	}
	if !claimed {
		return e.skip(run, e.overlapReason(a.ID), ResultSkipped)
	}
	defer func() {
		if err := e.store.ReleaseAutomation(a.ID, run.ID); err != nil {
			logger.Error("automation", "failed to release run guard", logger.F{"automation_id": a.ID, "run_id": run.ID, "error": err})
		}
	}()

	// Authorise against the run-as identity NOW, before the first side effect, so
	// a run is never half-executed for lack of permission. See authorize.go.
	user, err := e.store.GetUser(a.RunAsUserID)
	var authErr error
	if err != nil || user == nil {
		authErr = fmt.Errorf("run-as user #%d no longer exists", a.RunAsUserID)
	} else {
		authErr = Authorise(user, a.Trigger, a.Actions)
	}
	if authErr != nil {
		msg := "refused before any step ran: " + authErr.Error()
		return e.finish(run, finishPending(run.Steps, 0, "not run: the run was not authorised"), nil, &msg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.runBudget)
	defer cancel()

	steps := append([]structs.AutomationStepResult(nil), run.Steps...)
	var failedStep *int
	var runErr *string
	fail := func(step int) {
		if failedStep == nil {
			failedStep = &step
		}
	}

	for i, action := range a.Actions {
		// The run budget always halts, whatever continue_on_error says: it is the
		// bound that keeps one hung action from pinning a runner slot forever.
		if ctx.Err() != nil {
			msg := fmt.Sprintf("the run budget of %s was exhausted before step %d could start", e.runBudget, i+1)
			steps[i].Status = structs.AutomationStepFailed
			steps[i].Summary = "not started: run budget exhausted"
			steps[i].Error = &msg
			fail(i + 1)
			runErr = &msg
			steps = finishPending(steps, i+1, "not run: the run budget was exhausted")
			break
		}

		steps[i] = e.runStep(ctx, a, run, user, f, i, action)
		e.persistSteps(run.ID, steps)

		if steps[i].Status == structs.AutomationStepFailed {
			fail(i + 1)
			if !action.ContinueOnError {
				steps = finishPending(steps, i+1, fmt.Sprintf("not run: step %d failed", i+1))
				break
			}
		}
	}

	return e.finish(run, steps, failedStep, runErr)
}

func (e *Executor) runStep(ctx context.Context, a *structs.Automation, run *structs.AutomationRun, user *structs.User,
	f Firing, i int, action structs.AutomationAction) structs.AutomationStepResult {

	started := e.now().UTC()
	result := structs.AutomationStepResult{
		Step:            i + 1,
		Type:            action.Type,
		ContinueOnError: action.ContinueOnError,
		StartedAt:       &started,
	}

	// Authorise already rejected unknown types, so this error is defensive — but
	// it is still an error naming the type, never a skip.
	kind, err := kindFor(action.Type)
	var effect stepEffect
	if err == nil {
		stepCtx, cancel := context.WithTimeout(ctx, kind.timeout(action.Config))
		effect, err = kind.execute(stepCtx, e, action.Config)
		cancel()
	}

	result.DurationMs = e.now().Sub(started).Milliseconds()
	result.Summary = effect.summary
	if err != nil {
		msg := err.Error()
		result.Status = structs.AutomationStepFailed
		result.Error = &msg
		if result.Summary == "" {
			result.Summary = msg
		}
	} else {
		result.Status = structs.AutomationStepSucceeded
	}

	if auditErr := e.audit(a, run, user, f, i, effect, err); auditErr != nil {
		result.Summary += " (audit record could not be written)"
	}
	return result
}

// audit records one attempted action. The actor is the pair (run-as user,
// automation run): the user_id says whose authority was used, and
// automation_run_id says the call arrived via an automation — without it an
// automated redeploy reads exactly like that person clicking Recreate.
func (e *Executor) audit(a *structs.Automation, run *structs.AutomationRun, user *structs.User,
	f Firing, i int, effect stepEffect, stepErr error) error {

	if effect.auditAction == "" {
		return nil // nothing was attempted
	}
	outcome := "succeeded"
	if stepErr != nil {
		outcome = "failed: " + stepErr.Error()
	}
	details := fmt.Sprintf("via automation #%d %q, run #%d step %d/%d (%s, acting as %s): %s — %s",
		a.ID, a.Name, run.ID, i+1, len(a.Actions), run.TriggerSource, describeUser(user), effect.summary, outcome)

	resourceID := effect.resourceID
	if resourceID == nil && effect.resourceType == "automation" {
		resourceID = &a.ID
	}
	req := query.CreateAuditLogRequest{
		UserID:          &user.ID,
		Action:          effect.auditAction,
		ResourceType:    effect.resourceType,
		ResourceID:      resourceID,
		AutomationRunID: &run.ID,
		Details:         &details,
		IPAddress:       f.IP,
	}

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = e.store.CreateAuditLog(req); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	logger.Error("automation", "audit record could not be written", logger.F{"run_id": run.ID, "step": i + 1, "error": err})
	return err
}

func (e *Executor) persistSteps(runID int, steps []structs.AutomationStepResult) {
	if err := e.store.UpdateRun(runID, query.UpdateAutomationRunRequest{Steps: &steps}); err != nil {
		logger.Warn("automation", "failed to persist step progress", logger.F{"run_id": runID, "error": err})
	}
}

// finish records a run's terminal status. Any failed step, or a run-level
// error, fails the run — continue_on_error decides whether later steps run, not
// whether a failure counts.
func (e *Executor) finish(run *structs.AutomationRun, steps []structs.AutomationStepResult, failedStep *int, runErr *string) *Outcome {
	status := structs.AutomationRunSucceeded
	result := ResultSucceeded
	if failedStep != nil || runErr != nil {
		status = structs.AutomationRunFailed
		result = ResultFailed
	}

	if err := e.store.UpdateRun(run.ID, query.UpdateAutomationRunRequest{
		Status: &status, FailedStep: failedStep, Error: runErr, Steps: &steps, Finished: true,
	}); err != nil {
		logger.Error("automation", "failed to record run outcome", logger.F{"run_id": run.ID, "error": err})
	}

	finished := e.now().UTC()
	run.Status = status
	run.FailedStep = failedStep
	run.Error = runErr
	run.Steps = steps
	run.FinishedAt = &finished

	logger.Info("automation", "automation run finished", logger.F{
		"automation_id": run.AutomationID, "run_id": run.ID, "status": status, "source": run.TriggerSource,
	})
	return &Outcome{Result: result, Run: run}
}

// skip records a firing that deliberately did not run. It is a row, not a log
// line: a skip recorded only in logs is how "my automation quietly stopped"
// becomes a genre of incident.
func (e *Executor) skip(run *structs.AutomationRun, reason string, result Result) *Outcome {
	reason = truncate(reason, maxSkipReasonLen-3)
	status := structs.AutomationRunSkipped
	steps := finishPending(run.Steps, 0, "not run: "+reason)

	if err := e.store.UpdateRun(run.ID, query.UpdateAutomationRunRequest{
		Status: &status, SkipReason: &reason, Steps: &steps, Finished: true,
	}); err != nil {
		logger.Error("automation", "failed to record skipped run", logger.F{"run_id": run.ID, "error": err})
	}

	finished := e.now().UTC()
	run.Status = status
	run.SkipReason = &reason
	run.Steps = steps
	run.FinishedAt = &finished

	logger.Warn("automation", "automation firing skipped", logger.F{
		"automation_id": run.AutomationID, "run_id": run.ID, "reason": reason,
	})
	return &Outcome{Result: result, Run: run}
}

func (e *Executor) overlapReason(automationID int) string {
	current, err := e.store.GetAutomation(automationID)
	if err == nil && current != nil && current.RunningRunID != nil {
		since := ""
		if current.RunningSince != nil {
			since = " since " + current.RunningSince.UTC().Format(time.RFC3339)
		}
		return fmt.Sprintf("automation is already running (run #%d%s); this firing was skipped, not queued", *current.RunningRunID, since)
	}
	return "automation is already running; this firing was skipped, not queued"
}

func (e *Executor) slotsBusyReason() string {
	return fmt.Sprintf("all %d automation runner slots are busy; this firing was skipped, not queued", cap(e.slots))
}

func (e *Executor) tryAcquireSlot() bool {
	select {
	case e.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (e *Executor) releaseSlot() { <-e.slots }

// pendingSteps is the step list a run starts with: every step pending, so an
// in-progress run shows what it has yet to do.
func pendingSteps(actions []structs.AutomationAction) []structs.AutomationStepResult {
	steps := make([]structs.AutomationStepResult, len(actions))
	for i, action := range actions {
		steps[i] = structs.AutomationStepResult{
			Step:            i + 1,
			Type:            action.Type,
			Status:          structs.AutomationStepPending,
			ContinueOnError: action.ContinueOnError,
			Summary:         "pending",
		}
	}
	return steps
}

// finishPending turns every still-pending step from index `from` onwards into a
// skipped step carrying the reason it never ran. No finished run keeps a
// pending step.
func finishPending(steps []structs.AutomationStepResult, from int, reason string) []structs.AutomationStepResult {
	out := append([]structs.AutomationStepResult(nil), steps...)
	for i := from; i < len(out); i++ {
		if out[i].Status == structs.AutomationStepPending {
			out[i].Status = structs.AutomationStepSkipped
			out[i].Summary = reason
		}
	}
	return out
}
