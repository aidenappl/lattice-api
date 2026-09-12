package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/tools"
	"github.com/aidenappl/lattice-api/webhooks"
)

// ── fakes ───────────────────────────────────────────────────────────────────

// fakeStore is an in-memory Store. ClaimAutomation reproduces the predicate of
// query.ClaimAutomationRun and CreateRun reproduces the (automation_id,
// scheduled_at) unique key, so the guarantees tested here are the ones the
// database actually enforces rather than ones the fake invents.
type fakeStore struct {
	mu          sync.Mutex
	automations map[int]*structs.Automation
	users       map[int]*structs.User
	stacks      map[int]*structs.Stack
	containers  map[int][]structs.Container
	runs        map[int]*structs.AutomationRun
	slots       map[string]bool
	nextRunID   int
	audits      []query.CreateAuditLogRequest
	touches     int
	claims      int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		automations: map[int]*structs.Automation{},
		users:       map[int]*structs.User{},
		stacks:      map[int]*structs.Stack{},
		containers:  map[int][]structs.Container{},
		runs:        map[int]*structs.AutomationRun{},
		slots:       map[string]bool{},
	}
}

func (s *fakeStore) GetAutomation(id int) (*structs.Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.automations[id]
	if !ok || !a.Active {
		return nil, query.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (s *fakeStore) GetAutomationByWebhookHash(hash string) (*structs.Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.automations {
		if a.Active && a.WebhookTokenHash != nil && *a.WebhookTokenHash == hash {
			cp := *a
			return &cp, nil
		}
	}
	return nil, query.ErrNotFound
}

func (s *fakeStore) TouchWebhook(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touches++
	now := time.Now().UTC()
	s.automations[id].WebhookLastUsedAt = &now
	return nil
}

func (s *fakeStore) CreateRun(req query.CreateAutomationRunRequest) (*structs.AutomationRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.ScheduledAt != nil {
		key := fmt.Sprintf("%d@%s", req.AutomationID, req.ScheduledAt.UTC().Format(time.RFC3339))
		if s.slots[key] {
			return nil, false, nil
		}
		s.slots[key] = true
	}
	s.nextRunID++
	run := &structs.AutomationRun{
		ID:            s.nextRunID,
		AutomationID:  req.AutomationID,
		TriggerSource: req.TriggerSource,
		TriggerDetail: req.TriggerDetail,
		ScheduledAt:   req.ScheduledAt,
		Status:        req.Status,
		RunAsUserID:   req.RunAsUserID,
		TriggeredBy:   req.TriggeredBy,
		Steps:         append([]structs.AutomationStepResult(nil), req.Steps...),
		StartedAt:     req.StartedAt,
	}
	s.runs[run.ID] = run
	cp := *run
	return &cp, true, nil
}

func (s *fakeStore) UpdateRun(id int, req query.UpdateAutomationRunRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if req.Status != nil {
		run.Status = *req.Status
	}
	if req.SkipReason != nil {
		run.SkipReason = req.SkipReason
	}
	if req.FailedStep != nil {
		run.FailedStep = req.FailedStep
	}
	if req.Error != nil {
		run.Error = req.Error
	}
	if req.Steps != nil {
		run.Steps = append([]structs.AutomationStepResult(nil), (*req.Steps)...)
	}
	if req.Finished {
		now := time.Now().UTC()
		run.FinishedAt = &now
	}
	return nil
}

func (s *fakeStore) ListStuckRuns(cutoff time.Time) ([]structs.AutomationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []structs.AutomationRun
	for _, run := range s.runs {
		if run.Status == structs.AutomationRunInProgress && run.StartedAt.Before(cutoff) {
			out = append(out, *run)
		}
	}
	return out, nil
}

func (s *fakeStore) ClaimAutomation(automationID, runID int, staleBefore time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.automations[automationID]
	if !ok || !a.Active {
		return false, nil
	}
	if a.RunningRunID == nil || (a.RunningSince != nil && a.RunningSince.Before(staleBefore)) {
		now := time.Now().UTC()
		a.RunningRunID = &runID
		a.RunningSince = &now
		s.claims++
		return true, nil
	}
	return false, nil
}

func (s *fakeStore) ReleaseAutomation(automationID, runID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.automations[automationID]; ok && a.RunningRunID != nil && *a.RunningRunID == runID {
		a.RunningRunID = nil
		a.RunningSince = nil
	}
	return nil
}

func (s *fakeStore) GetUser(id int) (*structs.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return nil, query.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (s *fakeStore) GetStack(id int) (*structs.Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stacks[id]
	if !ok {
		// query.GetStackByID wraps sql.ErrNoRows rather than returning ErrNotFound.
		return nil, errors.New("failed to scan stack: sql: no rows in result set")
	}
	cp := *st
	return &cp, nil
}

func (s *fakeStore) ListStackContainers(stackID int) ([]structs.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]structs.Container(nil), s.containers[stackID]...), nil
}

func (s *fakeStore) CreateAuditLog(req query.CreateAuditLogRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, req)
	return nil
}

// writes counts every write the store has seen.
func (s *fakeStore) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.touches + len(s.runs) + s.claims + len(s.audits)
}

func (s *fakeStore) run(id int) structs.AutomationRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.runs[id]
}

func (s *fakeStore) runCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *fakeStore) auditLog() []query.CreateAuditLogRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]query.CreateAuditLogRequest(nil), s.audits...)
}

func (s *fakeStore) guard(id int) *int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.automations[id].RunningRunID
}

func (s *fakeStore) add(a structs.Automation) *structs.Automation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == 0 {
		a.ID = len(s.automations) + 1
	}
	if a.RunAsUserID == 0 {
		a.RunAsUserID = adminID
	}
	a.Active = true
	s.automations[a.ID] = &a
	cp := a
	return &cp
}

type redeployCall struct {
	containerID int
	workerID    int
	name        string
}

// fakeRedeployer records every recreate. When entered is set it signals on each
// call, and when release is set each call blocks until it is closed — which is
// how a test holds a run inside a step.
type fakeRedeployer struct {
	mu      sync.Mutex
	calls   []redeployCall
	failFor map[string]error
	entered chan struct{}
	release chan struct{}
}

func (f *fakeRedeployer) RecreateContainer(c *structs.Container, workerID int) error {
	f.mu.Lock()
	f.calls = append(f.calls, redeployCall{containerID: c.ID, workerID: workerID, name: c.Name})
	err := f.failFor[c.Name]
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return err
}

func (f *fakeRedeployer) called() []redeployCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]redeployCall(nil), f.calls...)
}

func (f *fakeRedeployer) names() []string {
	var out []string
	for _, c := range f.called() {
		out = append(out, c.name)
	}
	return out
}

// ── fixtures ────────────────────────────────────────────────────────────────

const (
	adminID  = 1
	editorID = 2
)

// fixture builds the incident's shape: two zones of one service, each in its
// own stack on its own worker, both containers called "monitor-core".
func fixture() (*fakeStore, *fakeRedeployer, *Executor) {
	s := newFakeStore()
	s.users[adminID] = &structs.User{ID: adminID, Email: "admin@example.com", Role: "admin", Active: true}
	s.users[editorID] = &structs.User{ID: editorID, Email: "editor@example.com", Role: "editor", Active: true}

	w11, w12 := 11, 12
	s.stacks[1] = &structs.Stack{ID: 1, Name: "monitor-zone-1", WorkerID: &w11, Active: true}
	s.stacks[2] = &structs.Stack{ID: 2, Name: "monitor-zone-2", WorkerID: &w12, Active: true}
	s.stacks[3] = &structs.Stack{ID: 3, Name: "unplaced", Active: true}
	s.containers[1] = []structs.Container{
		{ID: 101, StackID: 1, Name: "monitor-core"},
		{ID: 102, StackID: 1, Name: "a"},
		{ID: 103, StackID: 1, Name: "b"},
		{ID: 104, StackID: 1, Name: "c"},
	}
	s.containers[2] = []structs.Container{{ID: 201, StackID: 2, Name: "monitor-core"}}
	s.containers[3] = []structs.Container{{ID: 301, StackID: 3, Name: "lonely"}}

	r := &fakeRedeployer{failFor: map[string]error{}}
	e := NewExecutor(s, r)
	e.validateURL = func(string) error { return nil }
	e.doHTTP = func(context.Context, webhooks.Request) (*webhooks.Response, error) {
		return &webhooks.Response{StatusCode: 204}, nil
	}
	return s, r, e
}

func redeploy(stackID int, name string, continueOnError bool) structs.AutomationAction {
	cfg, _ := json.Marshal(structs.RedeployContainerConfig{StackID: stackID, ContainerName: name})
	return structs.AutomationAction{Type: structs.AutomationActionRedeployContainer, ContinueOnError: continueOnError, Config: cfg}
}

func httpCall(rawURL string, continueOnError bool) structs.AutomationAction {
	cfg, _ := json.Marshal(structs.HTTPRequestConfig{Method: "POST", URL: rawURL, Body: `{"text":"deployed"}`})
	return structs.AutomationAction{Type: structs.AutomationActionHTTPRequest, ContinueOnError: continueOnError, Config: cfg}
}

func webhookAutomation(actions ...structs.AutomationAction) structs.Automation {
	return structs.Automation{
		Name:    "redeploy monitor-core everywhere",
		Enabled: true,
		Trigger: structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook},
		Actions: actions,
	}
}

func manual() Firing {
	return Firing{Source: structs.AutomationSourceManual, Detail: "test"}
}

func strPtr(s string) *string { return &s }

func statuses(steps []structs.AutomationStepResult) []structs.AutomationStepStatus {
	out := make([]structs.AutomationStepStatus, len(steps))
	for i, st := range steps {
		out[i] = st.Status
	}
	return out
}

func equalSlices[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── tests ───────────────────────────────────────────────────────────────────

// Sequential execution with stop-on-first-failure is the default; the failing
// step is named. continue_on_error changes whether later steps run — never
// whether the run counts as failed.
func TestStepFailureHandling(t *testing.T) {
	const (
		ok      = structs.AutomationStepSucceeded
		failed  = structs.AutomationStepFailed
		skipped = structs.AutomationStepSkipped
	)
	tests := []struct {
		name           string
		actions        []structs.AutomationAction
		fail           []string
		wantResult     Result
		wantFailedStep int
		wantSteps      []structs.AutomationStepStatus
		wantCalls      []string
	}{
		{
			name:       "every step succeeds",
			actions:    []structs.AutomationAction{redeploy(1, "a", false), redeploy(1, "b", false), redeploy(1, "c", false)},
			wantResult: ResultSucceeded,
			wantSteps:  []structs.AutomationStepStatus{ok, ok, ok},
			wantCalls:  []string{"a", "b", "c"},
		},
		{
			name:           "stops at the first failure and names that step",
			actions:        []structs.AutomationAction{redeploy(1, "a", false), redeploy(1, "b", false), redeploy(1, "c", false)},
			fail:           []string{"b"},
			wantResult:     ResultFailed,
			wantFailedStep: 2,
			wantSteps:      []structs.AutomationStepStatus{ok, failed, skipped},
			wantCalls:      []string{"a", "b"},
		},
		{
			name:           "continue_on_error lets later steps run, and the run still fails",
			actions:        []structs.AutomationAction{redeploy(1, "a", false), redeploy(1, "b", true), redeploy(1, "c", false)},
			fail:           []string{"b"},
			wantResult:     ResultFailed,
			wantFailedStep: 2,
			wantSteps:      []structs.AutomationStepStatus{ok, failed, ok},
			wantCalls:      []string{"a", "b", "c"},
		},
		{
			name:       "continue_on_error on a step that succeeds changes nothing",
			actions:    []structs.AutomationAction{redeploy(1, "a", true), redeploy(1, "b", false), redeploy(1, "c", false)},
			wantResult: ResultSucceeded,
			wantSteps:  []structs.AutomationStepStatus{ok, ok, ok},
			wantCalls:  []string{"a", "b", "c"},
		},
		{
			name:           "the FIRST failure is the one named, even when a later step fails and halts",
			actions:        []structs.AutomationAction{redeploy(1, "a", true), redeploy(1, "b", false), redeploy(1, "c", false)},
			fail:           []string{"a", "b"},
			wantResult:     ResultFailed,
			wantFailedStep: 1,
			wantSteps:      []structs.AutomationStepStatus{failed, failed, skipped},
			wantCalls:      []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, r, e := fixture()
			for _, name := range tt.fail {
				r.failFor[name] = errors.New("worker not connected")
			}
			a := s.add(webhookAutomation(tt.actions...))

			out, err := e.Fire(a, manual())
			if err != nil {
				t.Fatalf("Fire: %v", err)
			}
			if out.Result != tt.wantResult {
				t.Errorf("result = %s, want %s", out.Result, tt.wantResult)
			}

			stored := s.run(out.Run.ID)
			if got := statuses(stored.Steps); !equalSlices(got, tt.wantSteps) {
				t.Errorf("step statuses = %v, want %v", got, tt.wantSteps)
			}
			switch {
			case tt.wantFailedStep == 0 && stored.FailedStep != nil:
				t.Errorf("failed_step = %d, want none", *stored.FailedStep)
			case tt.wantFailedStep != 0 && (stored.FailedStep == nil || *stored.FailedStep != tt.wantFailedStep):
				t.Errorf("failed_step = %v, want %d", stored.FailedStep, tt.wantFailedStep)
			}
			if got := r.names(); !equalSlices(got, tt.wantCalls) {
				t.Errorf("recreates sent = %v, want %v", got, tt.wantCalls)
			}
			for _, st := range stored.Steps {
				if st.Status == skipped && !strings.HasPrefix(st.Summary, "not run:") {
					t.Errorf("step %d was skipped without saying why: %q", st.Step, st.Summary)
				}
				if st.Status == structs.AutomationStepPending {
					t.Errorf("a finished run kept step %d pending", st.Step)
				}
			}
			if s.guard(a.ID) != nil {
				t.Error("the run guard was not released after the run finished")
			}
		})
	}
}

// An unknown action type fails loudly and names the type. It is never skipped:
// a step that silently does nothing reads exactly like a step that worked.
func TestUnknownActionTypeFailsLoudly(t *testing.T) {
	bogus := structs.AutomationAction{Type: "reboot_the_world", Config: json.RawMessage(`{}`)}

	t.Run("the dispatch switch names the type", func(t *testing.T) {
		_, err := kindFor(bogus.Type)
		if err == nil || !strings.Contains(err.Error(), `"reboot_the_world"`) {
			t.Fatalf("kindFor(%q) = %v, want an error naming the type", bogus.Type, err)
		}
	})

	t.Run("saving is refused, naming the step and the type", func(t *testing.T) {
		_, _, e := fixture()
		err := e.Validate(structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook},
			[]structs.AutomationAction{redeploy(1, "a", false), bogus})
		if err == nil || !strings.Contains(err.Error(), "step 2") || !strings.Contains(err.Error(), "reboot_the_world") {
			t.Fatalf("Validate = %v, want an error naming step 2 and the type", err)
		}
	})

	t.Run("a stored unknown type fails the run before any side effect", func(t *testing.T) {
		s, r, e := fixture()
		a := s.add(webhookAutomation(redeploy(1, "a", false), bogus))

		out, err := e.Fire(a, manual())
		if err != nil {
			t.Fatalf("Fire: %v", err)
		}
		if out.Result != ResultFailed {
			t.Fatalf("result = %s, want failed", out.Result)
		}
		if out.Run.Error == nil || !strings.Contains(*out.Run.Error, "reboot_the_world") {
			t.Errorf("run error = %v, want it to name the unknown type", out.Run.Error)
		}
		if calls := r.called(); len(calls) != 0 {
			t.Errorf("step 1 ran despite an unknown step 2: %v", calls)
		}
		if audits := s.auditLog(); len(audits) != 0 {
			t.Errorf("audit rows written for a run that did nothing: %d", len(audits))
		}
	})
}

// A disabled automation does not execute — whichever way it is fired — but the
// firing is still recorded, with the reason.
func TestDisabledAutomationDoesNotExecute(t *testing.T) {
	const token = "disabled-automation-token"
	fire := map[string]func(e *Executor, a *structs.Automation) structs.AutomationRun{
		"manual": func(e *Executor, a *structs.Automation) structs.AutomationRun {
			out, err := e.Fire(a, manual())
			if err != nil || out.Result != ResultDisabled {
				t.Fatalf("Fire = %+v, %v; want result disabled", out, err)
			}
			return *out.Run
		},
		"webhook": func(e *Executor, a *structs.Automation) structs.AutomationRun {
			out, err := e.FireWebhook(token, Firing{Source: structs.AutomationSourceWebhook})
			if err != nil || out.Result != ResultDisabled {
				t.Fatalf("FireWebhook = %+v, %v; want result disabled", out, err)
			}
			return *out.Run
		},
		"schedule": func(e *Executor, a *structs.Automation) structs.AutomationRun {
			<-e.FireScheduled(a, time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC), "")
			return e.store.(*fakeStore).run(1)
		},
	}

	for name, fireIt := range fire {
		t.Run(name, func(t *testing.T) {
			s, r, e := fixture()
			def := webhookAutomation(redeploy(1, "a", false))
			def.Enabled = false
			def.WebhookTokenHash = strPtr(tools.HashToken(token))
			a := s.add(def)

			run := fireIt(e, a)

			if run.Status != structs.AutomationRunSkipped {
				t.Errorf("run status = %s, want skipped", run.Status)
			}
			if run.SkipReason == nil || !strings.Contains(*run.SkipReason, "disabled") {
				t.Errorf("skip reason = %v, want it to say the automation is disabled", run.SkipReason)
			}
			if calls := r.called(); len(calls) != 0 {
				t.Errorf("a disabled automation sent %d recreate(s)", len(calls))
			}
			if s.claims != 0 {
				t.Error("a disabled automation took the run guard")
			}
			if audits := s.auditLog(); len(audits) != 0 {
				t.Errorf("a disabled automation wrote %d audit row(s)", len(audits))
			}
		})
	}
}

// One automation never runs twice at once. The second firing is skipped with a
// reason naming the run in progress — never queued, since a queue is how a
// retried webhook becomes a redeploy storm.
func TestConcurrencyGuard(t *testing.T) {
	t.Run("a second firing while the first is running is skipped, not queued", func(t *testing.T) {
		s, r, e := fixture()
		r.entered = make(chan struct{}, 1)
		r.release = make(chan struct{})
		a := s.add(webhookAutomation(redeploy(1, "a", false)))

		first := make(chan *Outcome, 1)
		go func() {
			out, _ := e.Fire(a, manual())
			first <- out
		}()
		<-r.entered // the first run is now inside its step, holding the guard

		second, err := e.Fire(a, manual())
		if err != nil {
			t.Fatalf("second Fire: %v", err)
		}
		if second.Result != ResultSkipped {
			t.Fatalf("second firing result = %s, want skipped", second.Result)
		}
		if second.Run.SkipReason == nil ||
			!strings.Contains(*second.Run.SkipReason, "already running") ||
			!strings.Contains(*second.Run.SkipReason, "run #1") {
			t.Errorf("skip reason = %v, want it to name run #1 as already running", second.Run.SkipReason)
		}

		close(r.release)
		if out := <-first; out == nil || out.Result != ResultSucceeded {
			t.Fatalf("first run = %+v, want succeeded", out)
		}
		if calls := r.called(); len(calls) != 1 {
			t.Fatalf("recreates sent = %d, want exactly 1 — the skipped firing must not run", len(calls))
		}

		// And the guard is free again once the first run ends.
		third, _ := e.Fire(a, manual())
		if third.Result != ResultSucceeded {
			t.Errorf("a firing after the first finished = %s, want succeeded", third.Result)
		}
	})

	t.Run("a fresh claim held by another run is respected", func(t *testing.T) {
		s, r, e := fixture()
		a := s.add(webhookAutomation(redeploy(1, "a", false)))
		holder, since := 999, time.Now().UTC().Add(-5*time.Second)
		s.automations[a.ID].RunningRunID, s.automations[a.ID].RunningSince = &holder, &since

		out, _ := e.Fire(a, manual())
		if out.Result != ResultSkipped || !strings.Contains(*out.Run.SkipReason, "run #999") {
			t.Errorf("result = %s (%v), want skipped naming run #999", out.Result, out.Run.SkipReason)
		}
		if len(r.called()) != 0 {
			t.Error("a skipped firing sent a recreate")
		}
	})

	t.Run("a claim older than the run budget is broken, so a crash cannot wedge the automation", func(t *testing.T) {
		s, _, e := fixture()
		a := s.add(webhookAutomation(redeploy(1, "a", false)))
		holder, since := 999, time.Now().UTC().Add(-CLAIM_STALE_AFTER-time.Minute)
		s.automations[a.ID].RunningRunID, s.automations[a.ID].RunningSince = &holder, &since

		if out, _ := e.Fire(a, manual()); out.Result != ResultSucceeded {
			t.Errorf("result = %s, want succeeded over a stale claim", out.Result)
		}
	})

	t.Run("no free runner slot is a skip, not a wait", func(t *testing.T) {
		s, r, e := fixture()
		e.slots = make(chan struct{}, 1)
		e.slots <- struct{}{} // every slot busy
		a := s.add(webhookAutomation(redeploy(1, "a", false)))

		out, _ := e.Fire(a, manual())
		if out.Result != ResultSkipped || !strings.Contains(*out.Run.SkipReason, "slots are busy") {
			t.Errorf("result = %s (%v), want skipped because the slots are busy", out.Result, out.Run.SkipReason)
		}
		if len(r.called()) != 0 {
			t.Error("a firing with no slot sent a recreate")
		}
	})
}

// An invalid, rotated-away or deleted webhook token is rejected, and NOTHING is
// written: no last-used touch, no run row, no claim, no audit.
func TestInvalidWebhookTokenIsRejectedWithoutSideEffects(t *testing.T) {
	s, r, e := fixture()

	live := webhookAutomation(redeploy(1, "a", false))
	live.WebhookTokenHash = strPtr(tools.HashToken("live-token")) // rotated from "old-token"
	s.add(live)

	deleted := webhookAutomation(redeploy(1, "a", false))
	deleted.WebhookTokenHash = strPtr(tools.HashToken("deleted-token"))
	d := s.add(deleted)
	s.automations[d.ID].Active = false

	scheduled := webhookAutomation(redeploy(1, "a", false))
	scheduled.Trigger = structs.AutomationTrigger{Type: structs.AutomationTriggerSchedule, Cron: "0 3 * * *"}
	scheduled.WebhookTokenHash = strPtr(tools.HashToken("schedule-token"))
	s.add(scheduled)

	tests := []struct {
		name  string
		token string
	}{
		{"empty token", ""},
		{"unknown token", "not-a-real-token"},
		{"token rotated away", "old-token"},
		{"token of a deleted automation", "deleted-token"},
		{"hash on an automation that is not webhook-triggered", "schedule-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := e.FireWebhook(tt.token, Firing{Source: structs.AutomationSourceWebhook})
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("FireWebhook(%q) error = %v, want ErrInvalidToken", tt.token, err)
			}
			if out != nil {
				t.Errorf("FireWebhook(%q) returned an outcome for a rejected token", tt.token)
			}
			if n := s.writes(); n != 0 {
				t.Errorf("a rejected token caused %d write(s)", n)
			}
			if len(r.called()) != 0 {
				t.Error("a rejected token sent a recreate")
			}
		})
	}

	t.Run("the live token does fire, and only it touches last_used_at", func(t *testing.T) {
		out, err := e.FireWebhook("live-token", Firing{Source: structs.AutomationSourceWebhook})
		if err != nil || out.Result != ResultSucceeded {
			t.Fatalf("FireWebhook(live) = %+v, %v; want succeeded", out, err)
		}
		if s.touches != 1 {
			t.Errorf("touches = %d, want 1", s.touches)
		}
	})
}

// The incident, as a test: one firing redeploys a container in each of two
// stacks on two different workers — both containers named "monitor-core" —
// with each step resolving its own stack and worker.
func TestCrossStackRedeployResolvesEachContainerIndependently(t *testing.T) {
	t.Run("two zones, one webhook", func(t *testing.T) {
		s, r, e := fixture()
		a := s.add(webhookAutomation(redeploy(1, "monitor-core", false), redeploy(2, "monitor-core", false)))

		out, err := e.Fire(a, manual())
		if err != nil || out.Result != ResultSucceeded {
			t.Fatalf("Fire = %+v, %v; want succeeded", out, err)
		}

		want := []redeployCall{
			{containerID: 101, workerID: 11, name: "monitor-core"},
			{containerID: 201, workerID: 12, name: "monitor-core"},
		}
		if got := r.called(); !equalSlices(got, want) {
			t.Fatalf("recreates = %+v, want %+v — each step must resolve its own stack and worker", got, want)
		}
		for i, wantIn := range []string{"monitor-zone-1", "monitor-zone-2"} {
			if !strings.Contains(out.Run.Steps[i].Summary, wantIn) {
				t.Errorf("step %d summary %q does not name stack %s", i+1, out.Run.Steps[i].Summary, wantIn)
			}
		}
	})

	t.Run("each step fails on its own terms and says which container", func(t *testing.T) {
		s, r, e := fixture()
		a := s.add(webhookAutomation(
			redeploy(1, "monitor-core", false),
			redeploy(3, "lonely", true),        // stack with no worker
			redeploy(2, "not-deployed", false), // no such container in zone 2
		))

		out, _ := e.Fire(a, manual())
		steps := out.Run.Steps
		if steps[1].Error == nil || !strings.Contains(*steps[1].Error, "no worker") {
			t.Errorf("step 2 error = %v, want the stack's missing worker named", steps[1].Error)
		}
		if steps[2].Error == nil || !strings.Contains(*steps[2].Error, `"not-deployed"`) || !strings.Contains(*steps[2].Error, "monitor-zone-2") {
			t.Errorf("step 3 error = %v, want the missing container and its stack named", steps[2].Error)
		}
		if got := r.called(); len(got) != 1 || got[0].containerID != 101 {
			t.Errorf("recreates = %+v, want only zone 1's container", got)
		}
	})
}

// Every action is authorised against the run-as identity at RUN time, so
// revoking or demoting a person stops their automations — before any step runs.
func TestRunTimeAuthorisation(t *testing.T) {
	schedule := structs.AutomationTrigger{Type: structs.AutomationTriggerSchedule, Cron: "0 3 * * *"}
	webhook := structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook}

	tests := []struct {
		name        string
		trigger     structs.AutomationTrigger
		actions     []structs.AutomationAction
		mutate      func(s *fakeStore)
		wantResult  Result
		errContains string
	}{
		{
			name:       "an active admin may run every step",
			trigger:    webhook,
			actions:    []structs.AutomationAction{redeploy(1, "a", false), httpCall("https://hooks.example.com/x", false)},
			wantResult: ResultSucceeded,
		},
		{
			name:        "deactivating the run-as user stops their automations",
			trigger:     webhook,
			actions:     []structs.AutomationAction{redeploy(1, "a", false)},
			mutate:      func(s *fakeStore) { s.users[adminID].Active = false },
			wantResult:  ResultFailed,
			errContains: "deactivated",
		},
		{
			name:        "deleting the run-as user stops their automations",
			trigger:     webhook,
			actions:     []structs.AutomationAction{redeploy(1, "a", false)},
			mutate:      func(s *fakeStore) { delete(s.users, adminID) },
			wantResult:  ResultFailed,
			errContains: "no longer exists",
		},
		{
			name:        "demotion stops the steps the new role cannot run",
			trigger:     schedule,
			actions:     []structs.AutomationAction{redeploy(1, "a", false), httpCall("https://hooks.example.com/x", false)},
			mutate:      func(s *fakeStore) { s.users[adminID].Role = "editor" },
			wantResult:  ResultFailed,
			errContains: "step 2 (http_request) requires the admin role",
		},
		{
			name:        "demotion also stops a webhook trigger, which holds a bearer credential",
			trigger:     webhook,
			actions:     []structs.AutomationAction{redeploy(1, "a", false)},
			mutate:      func(s *fakeStore) { s.users[adminID].Role = "editor" },
			wantResult:  ResultFailed,
			errContains: "webhook triggers require the admin role",
		},
		{
			name:        "a pending user can act as nobody",
			trigger:     schedule,
			actions:     []structs.AutomationAction{redeploy(1, "a", false)},
			mutate:      func(s *fakeStore) { s.users[adminID].Role = "pending" },
			wantResult:  ResultFailed,
			errContains: "cannot run automations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, r, e := fixture()
			httpCalls := 0
			e.doHTTP = func(context.Context, webhooks.Request) (*webhooks.Response, error) {
				httpCalls++
				return &webhooks.Response{StatusCode: 200}, nil
			}
			def := webhookAutomation(tt.actions...)
			def.Trigger = tt.trigger
			a := s.add(def)
			if tt.mutate != nil {
				tt.mutate(s)
			}

			out, err := e.Fire(a, manual())
			if err != nil {
				t.Fatalf("Fire: %v", err)
			}
			if out.Result != tt.wantResult {
				t.Fatalf("result = %s, want %s (error: %v)", out.Result, tt.wantResult, out.Run.Error)
			}
			if tt.errContains == "" {
				return
			}
			if out.Run.Error == nil || !strings.Contains(*out.Run.Error, tt.errContains) {
				t.Errorf("run error = %v, want it to contain %q", out.Run.Error, tt.errContains)
			}
			if len(r.called()) != 0 || httpCalls != 0 || len(s.auditLog()) != 0 {
				t.Errorf("a refused run had side effects: %d recreates, %d http calls, %d audit rows",
					len(r.called()), httpCalls, len(s.auditLog()))
			}
			for _, st := range out.Run.Steps {
				if st.Status != structs.AutomationStepSkipped {
					t.Errorf("step %d of a refused run is %s, want skipped", st.Step, st.Status)
				}
			}
		})
	}
}

// Authorise answers "may this person own this definition?" at save time with the
// same function that answers "may this run proceed?" at run time.
func TestAuthoriseMatchesTheRoutesItStandsIn(t *testing.T) {
	schedule := structs.AutomationTrigger{Type: structs.AutomationTriggerSchedule, Cron: "0 3 * * *"}
	webhook := structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook}
	redeployOnly := []structs.AutomationAction{redeploy(1, "a", false)}
	withHTTP := []structs.AutomationAction{redeploy(1, "a", false), httpCall("https://x.example.com", false)}

	tests := []struct {
		name    string
		role    string
		trigger structs.AutomationTrigger
		actions []structs.AutomationAction
		allowed bool
	}{
		{"editor: scheduled redeploy", "editor", schedule, redeployOnly, true},
		{"editor: webhook (a bearer credential, like a deploy token)", "editor", webhook, redeployOnly, false},
		{"editor: outbound http", "editor", schedule, withHTTP, false},
		{"viewer: cannot redeploy", "viewer", schedule, redeployOnly, false},
		{"admin: everything", "admin", webhook, withHTTP, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Authorise(&structs.User{ID: 7, Email: "u@example.com", Role: tt.role, Active: true}, tt.trigger, tt.actions)
			if (err == nil) != tt.allowed {
				t.Errorf("Authorise = %v, allowed want %v", err, tt.allowed)
			}
		})
	}
}

// Every attempted action writes an audit row whose actor records that it
// arrived via an automation — the run id — not merely whose authority it used.
func TestEveryActionIsAudited(t *testing.T) {
	s, r, e := fixture()
	r.failFor["b"] = errors.New("worker not connected")
	a := s.add(webhookAutomation(
		redeploy(1, "a", false),
		httpCall("https://hooks.example.com/services/T000/B000/SECRET123", false),
		redeploy(1, "b", true),
	))

	ip := "203.0.113.9"
	out, _ := e.Fire(a, Firing{Source: structs.AutomationSourceWebhook, IP: &ip})

	audits := s.auditLog()
	if len(audits) != 3 {
		t.Fatalf("audit rows = %d, want one per attempted action (3)", len(audits))
	}
	for i, row := range audits {
		if row.AutomationRunID == nil || *row.AutomationRunID != out.Run.ID {
			t.Errorf("row %d: automation_run_id = %v, want run #%d", i, row.AutomationRunID, out.Run.ID)
		}
		if row.UserID == nil || *row.UserID != adminID {
			t.Errorf("row %d: user_id = %v, want the run-as user", i, row.UserID)
		}
		if row.IPAddress == nil || *row.IPAddress != ip {
			t.Errorf("row %d: ip = %v, want the caller's", i, row.IPAddress)
		}
		if row.Details == nil || !strings.Contains(*row.Details, fmt.Sprintf("via automation #%d", a.ID)) {
			t.Errorf("row %d: details %v do not say the call came via the automation", i, row.Details)
		}
	}
	if audits[0].ResourceType != "container" || audits[0].ResourceID == nil || *audits[0].ResourceID != 102 {
		t.Errorf("redeploy audit resource = %s #%v, want container #102", audits[0].ResourceType, audits[0].ResourceID)
	}
	if audits[1].Action != "http_request" || strings.Contains(*audits[1].Details, "SECRET123") {
		t.Errorf("http audit row = %q; it must not carry the URL path, where chat webhooks keep their secret", *audits[1].Details)
	}
	if !strings.Contains(*audits[2].Details, "failed") {
		t.Errorf("a failed action's audit row does not say it failed: %q", *audits[2].Details)
	}
}

// Every action is bounded and so is the whole run: one hung HTTP call must not
// pin a runner slot forever.
func TestHungActionIsBoundedByTheRunBudget(t *testing.T) {
	s, r, e := fixture()
	e.runBudget = 150 * time.Millisecond
	e.doHTTP = func(ctx context.Context, _ webhooks.Request) (*webhooks.Response, error) {
		<-ctx.Done() // hang until the budget cuts it off
		return nil, ctx.Err()
	}
	a := s.add(webhookAutomation(httpCall("https://hangs.example.com", true), redeploy(1, "a", false)))

	started := time.Now()
	out, _ := e.Fire(a, manual())
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("a hung action held the run for %s — the budget did not bound it", elapsed)
	}

	if out.Result != ResultFailed {
		t.Fatalf("result = %s, want failed", out.Result)
	}
	steps := out.Run.Steps
	if steps[0].Status != structs.AutomationStepFailed {
		t.Errorf("hung step = %s, want failed", steps[0].Status)
	}
	if steps[1].Status != structs.AutomationStepFailed || steps[1].Error == nil || !strings.Contains(*steps[1].Error, "budget") {
		t.Errorf("step after an exhausted budget = %s (%v), want failed naming the budget", steps[1].Status, steps[1].Error)
	}
	if len(r.called()) != 0 {
		t.Error("a step ran after the run budget was exhausted, despite continue_on_error")
	}
	if len(e.slots) != 0 {
		t.Error("the runner slot was not released")
	}
	if s.guard(a.ID) != nil {
		t.Error("the run guard was not released")
	}
}

// A schedule slot fires once, however many scheduler ticks see it: the insert of
// its run row is the claim, as for snapshot runs.
func TestScheduledSlotFiresOnce(t *testing.T) {
	s, r, e := fixture()
	def := webhookAutomation(redeploy(1, "a", false))
	def.Trigger = structs.AutomationTrigger{Type: structs.AutomationTriggerSchedule, Cron: "* * * * *"}
	a := s.add(def)
	slot := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)

	<-e.FireScheduled(a, slot, "")
	<-e.FireScheduled(a, slot, "") // the next tick sees the same slot
	if n := s.runCount(); n != 1 {
		t.Fatalf("runs = %d after two ticks on one slot, want 1", n)
	}
	if n := len(r.called()); n != 1 {
		t.Fatalf("recreates = %d, want 1", n)
	}

	<-e.FireScheduled(a, slot.Add(time.Minute), "")
	if n := s.runCount(); n != 2 {
		t.Errorf("runs = %d after the next slot, want 2", n)
	}

	<-e.FireScheduled(a, slot.Add(2*time.Minute), "slot was 3h0m0s old; beyond the 2h0m0s catch-up window")
	stale := s.run(3)
	if stale.Status != structs.AutomationRunSkipped || stale.SkipReason == nil || !strings.Contains(*stale.SkipReason, "catch-up") {
		t.Errorf("stale slot run = %s (%v), want skipped with its reason", stale.Status, stale.SkipReason)
	}
	if n := len(r.called()); n != 2 {
		t.Errorf("a stale slot sent a recreate (total %d)", n)
	}
}

// A run left in progress by a control plane that died mid-run is failed with an
// explanation, and its run guard is released so the automation fires again.
func TestFailStuckRunsReleasesTheGuard(t *testing.T) {
	s, _, e := fixture()
	a := s.add(webhookAutomation(redeploy(1, "a", false), redeploy(1, "b", false)))

	run, _, _ := s.CreateRun(query.CreateAutomationRunRequest{
		AutomationID:  a.ID,
		TriggerSource: structs.AutomationSourceWebhook,
		Status:        structs.AutomationRunInProgress,
		Steps:         pendingSteps(a.Actions),
		StartedAt:     time.Now().UTC().Add(-CLAIM_STALE_AFTER - time.Minute),
	})
	since := run.StartedAt
	s.automations[a.ID].RunningRunID, s.automations[a.ID].RunningSince = &run.ID, &since

	e.FailStuckRuns()

	stuck := s.run(run.ID)
	if stuck.Status != structs.AutomationRunFailed || stuck.Error == nil || !strings.Contains(*stuck.Error, "restarted") {
		t.Errorf("stuck run = %s (%v), want failed with an explanation", stuck.Status, stuck.Error)
	}
	for _, st := range stuck.Steps {
		if st.Status != structs.AutomationStepSkipped {
			t.Errorf("step %d of an interrupted run is %s, want skipped", st.Step, st.Status)
		}
	}
	if s.guard(a.ID) != nil {
		t.Fatal("the stuck run's guard was not released")
	}
	if out, _ := e.Fire(a, manual()); out.Result != ResultSucceeded {
		t.Errorf("the automation did not fire again after the sweep: %s", out.Result)
	}
}

func TestValidate(t *testing.T) {
	webhook := structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook}
	raw := func(t structs.AutomationActionType, cfg string) structs.AutomationAction {
		return structs.AutomationAction{Type: t, Config: json.RawMessage(cfg)}
	}
	tooMany := make([]structs.AutomationAction, MAX_ACTIONS+1)
	for i := range tooMany {
		tooMany[i] = redeploy(1, "a", false)
	}

	tests := []struct {
		name        string
		trigger     structs.AutomationTrigger
		actions     []structs.AutomationAction
		errContains string // empty = valid
	}{
		{"webhook with one redeploy", webhook, []structs.AutomationAction{redeploy(1, "a", false)}, ""},
		{"schedule with redeploy and http", structs.AutomationTrigger{Type: structs.AutomationTriggerSchedule, Cron: "*/5 * * * *"},
			[]structs.AutomationAction{redeploy(2, "monitor-core", false), httpCall("https://x.example.com", false)}, ""},
		{"a webhook trigger takes no cron", structs.AutomationTrigger{Type: structs.AutomationTriggerWebhook, Cron: "* * * * *"},
			[]structs.AutomationAction{redeploy(1, "a", false)}, "no cron"},
		{"a cron that would never fire", structs.AutomationTrigger{Type: structs.AutomationTriggerSchedule, Cron: "60 * * * *"},
			[]structs.AutomationAction{redeploy(1, "a", false)}, "outside"},
		{"an unknown trigger type", structs.AutomationTrigger{Type: "email"},
			[]structs.AutomationAction{redeploy(1, "a", false)}, `"email"`},
		{"no actions", webhook, nil, "at least one action"},
		{"too many actions", webhook, tooMany, "at most"},
		{"a config typo is an error, not a silently ignored field", webhook,
			[]structs.AutomationAction{raw(structs.AutomationActionRedeployContainer, `{"stack_id":1,"container":"a"}`)}, "unknown field"},
		{"missing config", webhook, []structs.AutomationAction{raw(structs.AutomationActionRedeployContainer, ``)}, "config is required"},
		{"a container that is not in the stack", webhook, []structs.AutomationAction{redeploy(2, "a", false)}, `"a" was not found in stack`},
		{"a stack that does not exist", webhook, []structs.AutomationAction{redeploy(99, "a", false)}, "stack #99"},
		{"an unsupported method", webhook,
			[]structs.AutomationAction{raw(structs.AutomationActionHTTPRequest, `{"method":"TRACE","url":"https://x.example.com"}`)}, "TRACE"},
		{"a GET with a body", webhook,
			[]structs.AutomationAction{raw(structs.AutomationActionHTTPRequest, `{"method":"GET","url":"https://x.example.com","body":"x"}`)}, "cannot carry a body"},
		{"a timeout past the cap", webhook,
			[]structs.AutomationAction{raw(structs.AutomationActionHTTPRequest, `{"method":"POST","url":"https://x.example.com","timeout_seconds":31}`)}, "timeout_seconds"},
		{"a header value with a line break", webhook,
			[]structs.AutomationAction{raw(structs.AutomationActionHTTPRequest, `{"method":"POST","url":"https://x.example.com","headers":{"X-A":"b\r\nX-Injected: 1"}}`)}, "line break"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, e := fixture()
			err := e.Validate(tt.trigger, tt.actions)
			if tt.errContains == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want valid", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.errContains)
			}
		})
	}

	t.Run("the URL guard is applied to http_request", func(t *testing.T) {
		_, _, e := fixture()
		e.validateURL = func(string) error { return errors.New("URL must use HTTPS scheme") }
		err := e.Validate(webhook, []structs.AutomationAction{httpCall("http://10.0.0.1/admin", false)})
		if err == nil || !strings.Contains(err.Error(), "HTTPS") {
			t.Fatalf("Validate = %v, want the SSRF guard's refusal", err)
		}
	})
}

// Non-admins see the shape of an http_request, never its header values or body.
func TestRedactActions(t *testing.T) {
	cfg, _ := json.Marshal(structs.HTTPRequestConfig{
		Method:  "POST",
		URL:     "https://hooks.example.com/x",
		Headers: map[string]string{"Authorization": "Bearer s3cr3t"},
		Body:    `{"token":"s3cr3t"}`,
	})
	actions := []structs.AutomationAction{
		redeploy(1, "monitor-core", false),
		{Type: structs.AutomationActionHTTPRequest, Config: cfg},
		{Type: "future_type", Config: json.RawMessage(`{"password":"s3cr3t"}`)},
	}

	out := RedactActions(actions)
	for i, a := range out {
		if strings.Contains(string(a.Config), "s3cr3t") {
			t.Errorf("action %d leaked a secret after redaction: %s", i, a.Config)
		}
	}
	if string(out[0].Config) != string(actions[0].Config) {
		t.Error("a redeploy step has nothing secret and should be returned as-is")
	}
	if !strings.Contains(string(out[1].Config), "hooks.example.com") {
		t.Error("redaction removed the URL, which a viewer is allowed to see")
	}
	if !strings.Contains(string(actions[1].Config), "s3cr3t") {
		t.Error("RedactActions mutated its input")
	}
}
