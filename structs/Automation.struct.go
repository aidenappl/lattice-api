package structs

import (
	"encoding/json"
	"time"
)

// AutomationTriggerType is what fires an automation. v1 has exactly one trigger
// per automation (see AGENTS.md — "Automations" — for where more would go).
type AutomationTriggerType string

const (
	// AutomationTriggerWebhook fires on POST /api/automations/{token}.
	AutomationTriggerWebhook AutomationTriggerType = "webhook"
	// AutomationTriggerSchedule fires on a 5-field UTC cron expression.
	AutomationTriggerSchedule AutomationTriggerType = "schedule"
)

func (t AutomationTriggerType) IsValid() bool {
	switch t {
	case AutomationTriggerWebhook, AutomationTriggerSchedule:
		return true
	}
	return false
}

// AutomationTrigger is the persisted trigger shape (automations.trigger_config).
type AutomationTrigger struct {
	Type AutomationTriggerType `json:"type"`
	// Cron is a 5-field expression evaluated in UTC. Schedule triggers only.
	Cron string `json:"cron,omitempty"`
}

// AutomationActionType discriminates an action's config shape.
//
// ⚠️ There is deliberately no IsValid here. The executor's kindFor switch
// (automations/actions.go) is the ONE list of known action types; a second list
// here would be a second place to forget when adding a type, and the executor
// would still have to fail an unknown type loudly regardless.
type AutomationActionType string

const (
	AutomationActionRedeployContainer AutomationActionType = "redeploy_container"
	AutomationActionHTTPRequest       AutomationActionType = "http_request"
)

// AutomationAction is one ordered step. Config is kept raw and decoded by the
// executor for the step's type, which is what lets a new action type be a new
// config shape rather than a schema change.
type AutomationAction struct {
	Type AutomationActionType `json:"type"`
	// ContinueOnError lets later steps run after this one fails. It controls
	// flow, not the verdict: a run with any failed step is still `failed`.
	ContinueOnError bool            `json:"continue_on_error"`
	Config          json.RawMessage `json:"config"`
}

// RedeployContainerConfig names a container by (stack, name), not by id.
//
// A container id does not survive a compose edit: HandleUpdateCompose
// soft-deletes every container row in the stack and re-inserts them, so an
// id-based step would break the first time anyone touched the compose file. A
// name alone is not enough either, because container names are only unique
// within a stack — two zones of one service are commonly both "monitor-core".
// The pair is stable across compose edits and unambiguous across stacks.
type RedeployContainerConfig struct {
	StackID       int    `json:"stack_id"`
	ContainerName string `json:"container_name"`
}

// HTTPRequestConfig is an outbound request made through webhooks.Deliver.
//
// ⚠️ Header values and the body are stored in plaintext in automations.actions
// and are redacted in API responses for non-admins only. Do not put long-lived
// credentials here; see AGENTS.md.
type HTTPRequestConfig struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           string            `json:"body,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type Automation struct {
	ID                int                `json:"id"`
	Name              string             `json:"name"`
	Description       *string            `json:"description"`
	Enabled           bool               `json:"enabled"`
	Trigger           AutomationTrigger  `json:"trigger"`
	Actions           []AutomationAction `json:"actions"`
	WebhookTokenHash  *string            `json:"-"`
	WebhookLastUsedAt *time.Time         `json:"webhook_last_used_at"`
	// RunAsUserID is the identity every action is authorised against at run
	// time — whoever last created, edited or enabled the automation.
	RunAsUserID  int        `json:"run_as_user_id"`
	CreatedBy    int        `json:"created_by"`
	RunningRunID *int       `json:"running_run_id"`
	RunningSince *time.Time `json:"running_since"`
	Active       bool       `json:"active"`
	UpdatedAt    time.Time  `json:"updated_at"`
	InsertedAt   time.Time  `json:"inserted_at"`

	// Populated by the handlers for display; not stored on the row.
	RunAs   *AutomationActor `json:"run_as,omitempty"`
	LastRun *AutomationRun   `json:"last_run,omitempty"`
}

// AutomationActor is the public face of an automation's run-as user. It is
// returned so the dashboard can say "this automation will fail: its run-as user
// is deactivated" before the next firing proves it.
type AutomationActor struct {
	ID     int     `json:"id"`
	Email  string  `json:"email"`
	Name   *string `json:"name"`
	Role   string  `json:"role"`
	Active bool    `json:"active"`
}

// AutomationTriggerSource records what caused one run.
type AutomationTriggerSource string

const (
	AutomationSourceWebhook  AutomationTriggerSource = "webhook"
	AutomationSourceSchedule AutomationTriggerSource = "schedule"
	AutomationSourceManual   AutomationTriggerSource = "manual"
)

// AutomationRunStatus is the lifecycle of one firing.
//
// `skipped` is a first-class outcome, as it is for snapshot runs: a firing that
// deliberately did not run (disabled, overlapping, no free slot, stale slot) is
// the thing an operator most needs to be able to see.
type AutomationRunStatus string

const (
	AutomationRunInProgress AutomationRunStatus = "in_progress"
	AutomationRunSucceeded  AutomationRunStatus = "succeeded"
	AutomationRunFailed     AutomationRunStatus = "failed"
	AutomationRunSkipped    AutomationRunStatus = "skipped"
)

func (s AutomationRunStatus) IsValid() bool {
	switch s {
	case AutomationRunInProgress, AutomationRunSucceeded, AutomationRunFailed, AutomationRunSkipped:
		return true
	}
	return false
}

// AutomationStepStatus is the outcome of one step inside a run.
type AutomationStepStatus string

const (
	// AutomationStepPending is a step not yet reached in a run still in progress.
	// No finished run keeps a pending step: finishing turns each into skipped,
	// with the reason it never ran.
	AutomationStepPending   AutomationStepStatus = "pending"
	AutomationStepSucceeded AutomationStepStatus = "succeeded"
	AutomationStepFailed    AutomationStepStatus = "failed"
	// AutomationStepSkipped means the step was never attempted — an earlier step
	// failed without continue_on_error, the run budget ran out, or the run was
	// refused before its first side effect.
	AutomationStepSkipped AutomationStepStatus = "skipped"
)

// AutomationStepResult is one step's entry in automation_runs.steps.
type AutomationStepResult struct {
	// Step is 1-based, matching how the failing step is named everywhere else.
	Step            int                  `json:"step"`
	Type            AutomationActionType `json:"type"`
	Status          AutomationStepStatus `json:"status"`
	ContinueOnError bool                 `json:"continue_on_error"`
	Summary         string               `json:"summary"`
	Error           *string              `json:"error,omitempty"`
	StartedAt       *time.Time           `json:"started_at,omitempty"`
	DurationMs      int64                `json:"duration_ms"`
}

type AutomationRun struct {
	ID            int                     `json:"id"`
	AutomationID  int                     `json:"automation_id"`
	TriggerSource AutomationTriggerSource `json:"trigger_source"`
	TriggerDetail *string                 `json:"trigger_detail"`
	// ScheduledAt is the nominal, un-jittered UTC slot for schedule runs and the
	// idempotency key; nil for webhook and manual runs.
	ScheduledAt *time.Time             `json:"scheduled_at"`
	Status      AutomationRunStatus    `json:"status"`
	SkipReason  *string                `json:"skip_reason"`
	FailedStep  *int                   `json:"failed_step"`
	Error       *string                `json:"error"`
	Steps       []AutomationStepResult `json:"steps"`
	RunAsUserID *int                   `json:"run_as_user_id"`
	TriggeredBy *int                   `json:"triggered_by"`
	StartedAt   time.Time              `json:"started_at"`
	FinishedAt  *time.Time             `json:"finished_at"`
	InsertedAt  time.Time              `json:"inserted_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}
