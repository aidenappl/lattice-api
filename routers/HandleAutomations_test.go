package routers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/automations"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/gorilla/mux"
)

// webhookStore is the smallest automations.Store the webhook handler can be
// exercised against: one automation, one stack, one user, and a count of every
// write the handler causes.
type webhookStore struct {
	automation *structs.Automation
	stack      *structs.Stack
	containers []structs.Container
	user       *structs.User
	busy       bool // another run holds the guard
	runs       map[int]*structs.AutomationRun
	writes     int
}

func (s *webhookStore) GetAutomation(id int) (*structs.Automation, error) {
	if s.automation == nil || id != s.automation.ID {
		return nil, query.ErrNotFound
	}
	cp := *s.automation
	if s.busy {
		holder := 41
		cp.RunningRunID = &holder
	}
	return &cp, nil
}

func (s *webhookStore) GetAutomationByWebhookHash(hash string) (*structs.Automation, error) {
	if s.automation == nil || s.automation.WebhookTokenHash == nil || *s.automation.WebhookTokenHash != hash {
		return nil, query.ErrNotFound
	}
	cp := *s.automation
	return &cp, nil
}

func (s *webhookStore) TouchWebhook(int) error { s.writes++; return nil }

func (s *webhookStore) CreateRun(req query.CreateAutomationRunRequest) (*structs.AutomationRun, bool, error) {
	s.writes++
	run := &structs.AutomationRun{
		ID:            len(s.runs) + 1,
		AutomationID:  req.AutomationID,
		TriggerSource: req.TriggerSource,
		Status:        req.Status,
		Steps:         req.Steps,
		StartedAt:     req.StartedAt,
	}
	s.runs[run.ID] = run
	cp := *run
	return &cp, true, nil
}

func (s *webhookStore) UpdateRun(id int, req query.UpdateAutomationRunRequest) error {
	s.writes++
	if req.Status != nil {
		s.runs[id].Status = *req.Status
	}
	return nil
}

func (s *webhookStore) ListStuckRuns(time.Time) ([]structs.AutomationRun, error) { return nil, nil }

func (s *webhookStore) ClaimAutomation(int, int, time.Time) (bool, error) {
	if s.busy {
		return false, nil
	}
	s.writes++
	return true, nil
}

func (s *webhookStore) ReleaseAutomation(int, int) error { return nil }

func (s *webhookStore) GetUser(int) (*structs.User, error) { return s.user, nil }

func (s *webhookStore) GetStack(id int) (*structs.Stack, error) {
	if s.stack == nil || id != s.stack.ID {
		return nil, errors.New("sql: no rows in result set")
	}
	return s.stack, nil
}

func (s *webhookStore) ListStackContainers(int) ([]structs.Container, error) {
	return s.containers, nil
}

func (s *webhookStore) CreateAuditLog(query.CreateAuditLogRequest) error { s.writes++; return nil }

type acceptingRedeployer struct{}

func (acceptingRedeployer) RecreateContainer(*structs.Container, int) error { return nil }

// The webhook's status codes are its contract with CI: a failure must turn a
// pipeline red without inviting an automatic retry, and a disabled automation
// must say so rather than 404 or silently succeed.
func TestAutomationWebhookResponses(t *testing.T) {
	const token = "the-webhook-token"

	tests := []struct {
		name          string
		token         string
		enabled       bool
		busy          bool
		container     string
		wantStatus    int
		wantResult    string
		wantCode      int
		wantInMessage string
		wantNoWrites  bool
	}{
		{
			name: "an unknown token is 401 and writes nothing", token: "not-the-token", enabled: true,
			container: "monitor-core", wantStatus: http.StatusUnauthorized, wantInMessage: "invalid automation token", wantNoWrites: true,
		},
		{
			name: "a disabled automation is 200 with an explicit disabled result — not a 404", token: token, enabled: false,
			container: "monitor-core", wantStatus: http.StatusOK, wantResult: "disabled", wantInMessage: "disabled",
		},
		{
			name: "an overlapping firing is 409, recorded rather than queued", token: token, enabled: true, busy: true,
			container: "monitor-core", wantStatus: http.StatusConflict, wantCode: errCodeAutomationSkipped, wantInMessage: "already running",
		},
		{
			name: "a failed step is 424 naming the run and the step", token: token, enabled: true,
			container: "not-deployed", wantStatus: http.StatusFailedDependency, wantCode: errCodeAutomationFailed, wantInMessage: "failed at step 1",
		},
		{
			name: "a successful run is 200", token: token, enabled: true,
			container: "monitor-core", wantStatus: http.StatusOK, wantResult: "succeeded", wantInMessage: "succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := 4
			cfg, _ := json.Marshal(structs.RedeployContainerConfig{StackID: 2, ContainerName: tt.container})
			hash := tools.HashToken(token)
			store := &webhookStore{
				automation: &structs.Automation{
					ID:               7,
					Name:             "redeploy monitor-core everywhere",
					Enabled:          tt.enabled,
					Active:           true,
					Trigger:          structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook},
					Actions:          []structs.AutomationAction{{Type: structs.AutomationActionRedeployContainer, Config: cfg}},
					WebhookTokenHash: &hash,
					RunAsUserID:      1,
				},
				stack:      &structs.Stack{ID: 2, Name: "monitor-zone-2", WorkerID: &worker, Active: true},
				containers: []structs.Container{{ID: 201, StackID: 2, Name: "monitor-core"}},
				user:       &structs.User{ID: 1, Email: "admin@example.com", Role: "admin", Active: true},
				busy:       tt.busy,
				runs:       map[int]*structs.AutomationRun{},
			}
			h := &AutomationHandler{Executor: automations.NewExecutor(store, acceptingRedeployer{})}

			router := mux.NewRouter()
			router.HandleFunc("/api/automations/{token}", h.HandleAutomationWebhook).Methods(http.MethodPost)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/automations/"+tt.token+"?commit=abc123", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var body struct {
				Message      string `json:"message"`
				ErrorMessage string `json:"error_message"`
				ErrorCode    int    `json:"error_code"`
				Data         struct {
					Result string `json:"result"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if tt.wantResult != "" && body.Data.Result != tt.wantResult {
				t.Errorf("data.result = %q, want %q", body.Data.Result, tt.wantResult)
			}
			if tt.wantCode != 0 && body.ErrorCode != tt.wantCode {
				t.Errorf("error_code = %d, want %d", body.ErrorCode, tt.wantCode)
			}
			if msg := body.Message + body.ErrorMessage; !strings.Contains(msg, tt.wantInMessage) {
				t.Errorf("message %q does not contain %q", msg, tt.wantInMessage)
			}
			if tt.wantNoWrites && store.writes != 0 {
				t.Errorf("a rejected token caused %d write(s)", store.writes)
			}
		})
	}
}
