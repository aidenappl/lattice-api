package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/structs"
	"github.com/aidenappl/lattice-api/webhooks"
)

// actionKind is one action type: the role it needs, how its config is checked
// when saved, how long it may run, how it runs, and what of its config a
// non-admin may see.
type actionKind interface {
	requiredRole() string
	validate(e *Executor, config json.RawMessage) error
	timeout(config json.RawMessage) time.Duration
	execute(ctx context.Context, e *Executor, config json.RawMessage) (stepEffect, error)
	redact(config json.RawMessage) json.RawMessage
}

// kindFor is THE dispatch point for action types, and the only list of them.
//
// Adding an action type is: a new case here, a config struct in
// structs/Automation.struct.go, and a type implementing actionKind. Nothing else
// — no migration (config is JSON), no handler change, no second list to keep in
// step with this one.
//
// ⚠️ An unknown type is an ERROR that names the type, never a skip. A step that
// silently does nothing reads in the run history exactly like a step that
// worked, which is the invisible-failure shape this feature exists to end.
func kindFor(t structs.AutomationActionType) (actionKind, error) {
	switch t {
	case structs.AutomationActionRedeployContainer:
		return redeployContainer{}, nil
	case structs.AutomationActionHTTPRequest:
		return httpRequest{}, nil
	default:
		return nil, fmt.Errorf("unknown action type %q", t)
	}
}

// stepEffect is what a step did, in the terms the run history and the audit log
// need. It is returned alongside an error, so a failed step still says what it
// was attempting.
type stepEffect struct {
	summary      string
	auditAction  string
	resourceType string
	resourceID   *int
}

// decodeConfig decodes strictly: an unknown field is an error, so a typo such as
// "container" for "container_name" fails when the automation is saved, instead
// of saving a step that can never resolve.
func decodeConfig[T any](raw json.RawMessage) (T, error) {
	var cfg T
	if len(raw) == 0 || string(raw) == "null" {
		return cfg, errors.New("config is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// ── redeploy_container ──────────────────────────────────────────────────────

// redeployContainer recreates one container — pull its image:tag and replace
// it — on whichever worker runs its stack. It is the same command the dashboard's
// Recreate button and a deploy token's ?container= send.
type redeployContainer struct{}

func (redeployContainer) requiredRole() string { return roleEditor }

func (redeployContainer) timeout(json.RawMessage) time.Duration { return REDEPLOY_ACTION_TIMEOUT }

func (redeployContainer) redact(config json.RawMessage) json.RawMessage { return config }

func (redeployContainer) validate(e *Executor, config json.RawMessage) error {
	cfg, err := decodeConfig[structs.RedeployContainerConfig](config)
	if err != nil {
		return err
	}
	if cfg.StackID <= 0 {
		return errors.New("stack_id is required")
	}
	if strings.TrimSpace(cfg.ContainerName) == "" {
		return errors.New("container_name is required")
	}
	_, _, err = e.resolveContainer(cfg)
	return err
}

func (redeployContainer) execute(ctx context.Context, e *Executor, config json.RawMessage) (stepEffect, error) {
	effect := stepEffect{auditAction: "recreate", resourceType: "container"}

	cfg, err := decodeConfig[structs.RedeployContainerConfig](config)
	if err != nil {
		return effect, err
	}
	effect.summary = fmt.Sprintf("recreate stack #%d / %s", cfg.StackID, cfg.ContainerName)

	stack, container, err := e.resolveContainer(cfg)
	if err != nil {
		return effect, err
	}
	effect.resourceID = &container.ID
	effect.summary = fmt.Sprintf("recreate %s/%s (container #%d)", stack.Name, container.Name, container.ID)

	if stack.WorkerID == nil {
		return effect, fmt.Errorf("stack %q has no worker assigned", stack.Name)
	}
	if err := ctx.Err(); err != nil {
		return effect, err
	}
	if err := e.redeployer.RecreateContainer(container, *stack.WorkerID); err != nil {
		return effect, fmt.Errorf("worker #%d did not accept the recreate for %s/%s: %w", *stack.WorkerID, stack.Name, container.Name, err)
	}

	// "Dispatched", not "redeployed": the worker has accepted the command, and
	// the runner performs the pull and replace asynchronously. Waiting for the
	// container to report healthy is a seam, not v1 — see AGENTS.md.
	effect.summary = fmt.Sprintf("recreate dispatched: %s/%s (container #%d) to worker #%d",
		stack.Name, container.Name, container.ID, *stack.WorkerID)
	return effect, nil
}

// resolveContainer finds the container a step names, at the moment it runs.
//
// Every step resolves its own stack and its own worker. That is the whole point
// of the action: a deploy token cannot reach past its one stack, and a step here
// is not scoped to anything but the container it names.
func (e *Executor) resolveContainer(cfg structs.RedeployContainerConfig) (*structs.Stack, *structs.Container, error) {
	stack, err := e.store.GetStack(cfg.StackID)
	if err != nil || stack == nil {
		return nil, nil, fmt.Errorf("stack #%d was not found (deleted?)", cfg.StackID)
	}
	containers, err := e.store.ListStackContainers(stack.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("could not list the containers of stack %q: %w", stack.Name, err)
	}
	for i := range containers {
		if containers[i].Name == cfg.ContainerName {
			return stack, &containers[i], nil
		}
	}
	return nil, nil, fmt.Errorf("container %q was not found in stack %q (#%d)", cfg.ContainerName, stack.Name, stack.ID)
}

// ── http_request ────────────────────────────────────────────────────────────

// httpRequest makes one outbound request through webhooks.Deliver — the same
// SSRF-guarded path event webhooks use — and succeeds only on a 2xx.
type httpRequest struct{}

var httpRequestMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
	http.MethodHead:   true,
}

// REDACTED replaces header values and bodies in responses to non-admins.
const REDACTED = "[redacted]"

func (httpRequest) requiredRole() string { return roleAdmin }

func (httpRequest) timeout(config json.RawMessage) time.Duration {
	cfg, err := decodeConfig[structs.HTTPRequestConfig](config)
	if err != nil || cfg.TimeoutSeconds <= 0 {
		return HTTP_ACTION_DEFAULT_TIMEOUT
	}
	if d := time.Duration(cfg.TimeoutSeconds) * time.Second; d < HTTP_ACTION_MAX_TIMEOUT {
		return d
	}
	return HTTP_ACTION_MAX_TIMEOUT
}

func (httpRequest) validate(e *Executor, config json.RawMessage) error {
	cfg, err := decodeConfig[structs.HTTPRequestConfig](config)
	if err != nil {
		return err
	}
	method := strings.ToUpper(cfg.Method)
	if !httpRequestMethods[method] {
		return fmt.Errorf("method %q is not one of GET, POST, PUT, PATCH, DELETE, HEAD", cfg.Method)
	}
	if err := e.validateURL(cfg.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	for name, value := range cfg.Headers {
		if name == "" || strings.ContainsAny(name, " \t\r\n:") {
			return fmt.Errorf("header name %q is not valid", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a line break", name)
		}
	}
	if len(cfg.Body) > MAX_HTTP_BODY_BYTES {
		return fmt.Errorf("body is %d bytes; the limit is %d", len(cfg.Body), MAX_HTTP_BODY_BYTES)
	}
	if cfg.Body != "" && (method == http.MethodGet || method == http.MethodHead) {
		return fmt.Errorf("%s requests cannot carry a body", method)
	}
	if cfg.TimeoutSeconds < 0 || time.Duration(cfg.TimeoutSeconds)*time.Second > HTTP_ACTION_MAX_TIMEOUT {
		return fmt.Errorf("timeout_seconds must be between 0 and %d", int(HTTP_ACTION_MAX_TIMEOUT/time.Second))
	}
	return nil
}

func (k httpRequest) execute(ctx context.Context, e *Executor, config json.RawMessage) (stepEffect, error) {
	effect := stepEffect{auditAction: "http_request", resourceType: "automation"}

	cfg, err := decodeConfig[structs.HTTPRequestConfig](config)
	if err != nil {
		return effect, err
	}
	method := strings.ToUpper(cfg.Method)
	target := describeURL(cfg.URL)
	effect.summary = fmt.Sprintf("%s %s", method, target)

	headers := make(map[string]string, len(cfg.Headers)+2)
	headers["User-Agent"] = "Lattice-Automation/1.0"
	if cfg.Body != "" {
		headers["Content-Type"] = "application/json"
	}
	for name, value := range cfg.Headers {
		headers[name] = value
	}

	resp, err := e.doHTTP(ctx, webhooks.Request{
		Method:  method,
		URL:     cfg.URL,
		Headers: headers,
		Body:    []byte(cfg.Body),
		Timeout: k.timeout(config),
	})
	if err != nil {
		effect.summary = fmt.Sprintf("%s %s → no response", method, target)
		return effect, fmt.Errorf("%s %s: %w", method, target, err)
	}

	effect.summary = fmt.Sprintf("%s %s → %d", method, target, resp.StatusCode)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return effect, fmt.Errorf("%s %s returned %d: %s", method, target, resp.StatusCode, truncate(strings.TrimSpace(resp.Body), 200))
	}
	return effect, nil
}

func (httpRequest) redact(config json.RawMessage) json.RawMessage {
	var cfg structs.HTTPRequestConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		// Unreadable is not a reason to leak it.
		return json.RawMessage(`{"redacted":true}`)
	}
	for name := range cfg.Headers {
		cfg.Headers[name] = REDACTED
	}
	if cfg.Body != "" {
		cfg.Body = REDACTED
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	return out
}

// describeURL is how a request target appears in run history and the audit
// log: scheme and host only. The path is dropped on purpose — Slack, Discord and
// most chat webhooks carry their secret IN the path, and the history must not
// become the place a secret leaks to.
func describeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparseable url)"
	}
	return u.Scheme + "://" + u.Host
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
