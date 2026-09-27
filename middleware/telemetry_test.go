package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/responder"
	"github.com/gorilla/mux"
)

// serveTelemetry runs one request through the production middleware order.
func serveTelemetry(t *testing.T, h http.HandlerFunc, target string) (*httptest.ResponseRecorder, *monitor.Recorder) {
	t.Helper()
	rec := monitor.StartRecording()
	t.Cleanup(rec.Stop)

	r := mux.NewRouter()
	r.Use(RequestIDMiddleware)
	r.Use(LoggingMiddleware)
	r.Use(RecoverMiddleware)
	r.Use(MuxHeaderMiddleware)
	r.HandleFunc("/admin/stacks/{id}", h)
	r.HandleFunc("/api/deploy/{token}", h)
	r.HandleFunc("/healthcheck", h)

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, target, nil))
	return rw, rec
}

func requestEvent(t *testing.T, rec *monitor.Recorder) monitor.Event {
	t.Helper()
	evs := rec.Named("http.request.end")
	if len(evs) != 1 {
		t.Fatalf("recorded %d http.request.end events, want 1", len(evs))
	}
	return evs[0]
}

func TestRequestEventCarriesRouteStatusAndRequestID(t *testing.T) {
	rw, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, "/admin/stacks/42")

	e := requestEvent(t, rec)
	d := e.Data.(map[string]any)
	if d["path"] != "/admin/stacks/{id}" || d["request_path"] != "/admin/stacks/42" || d["status_code"] != http.StatusNoContent {
		t.Errorf("data = %v", d)
	}
	if e.Level != monitor.LevelInfo {
		t.Errorf("level = %q, want info", e.Level)
	}
	if id := rw.Header().Get("X-Request-ID"); id == "" || e.RequestID != id {
		t.Errorf("event request_id %q should match the X-Request-ID header %q", e.RequestID, id)
	}
}

func TestDeployTokenStaysOutOfTheGroupingKey(t *testing.T) {
	_, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {
		responder.SendError(w, http.StatusUnauthorized, "invalid deploy token")
	}, "/api/deploy/dtk_0123456789abcdef")

	if d := requestEvent(t, rec).Data.(map[string]any); d["path"] != "/api/deploy/{token}" {
		t.Errorf("path = %v; the route template, never the token, is what issues group by", d["path"])
	}
}

func TestServerFailureCarriesTheInternalError(t *testing.T) {
	rw, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {
		responder.QueryError(w, errors.New("dial tcp 10.0.0.5:3306: connect: connection refused"), "failed to get stack")
	}, "/admin/stacks/1")

	e := requestEvent(t, rec)
	d := e.Data.(map[string]any)
	if e.Level != monitor.LevelError || d["status_code"] != http.StatusInternalServerError {
		t.Errorf("level=%q status=%v", e.Level, d["status_code"])
	}
	if !strings.Contains(d["error"].(string), "connection refused") || d["error_message"] != "failed to get stack" {
		t.Errorf("failure not attached: %v", d)
	}
	// Monitor sees the cause; the client still does not.
	if strings.Contains(rw.Body.String(), "connection refused") {
		t.Errorf("5xx body leaked the internal error: %s", rw.Body.String())
	}
}

func TestClientFailureIsAWarning(t *testing.T) {
	_, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {
		responder.SendErrorWithCode(w, http.StatusForbidden, "your account is pending admin approval", 4004)
	}, "/admin/stacks/1")

	e := requestEvent(t, rec)
	d := e.Data.(map[string]any)
	if e.Level != monitor.LevelWarn || d["error_message"] != "your account is pending admin approval" || d["error_code"] != 4004 {
		t.Errorf("level=%q data=%v", e.Level, d)
	}
}

func TestQueryStringNeverReachesTheEvent(t *testing.T) {
	_, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {}, "/admin/stacks/1?code=SplxlOBeZQQYbYS6WxSbIA&state=xyz")

	b, _ := json.Marshal(requestEvent(t, rec))
	if strings.Contains(string(b), "SplxlOBe") {
		t.Errorf("event leaked the query string: %s", b)
	}
}

func TestSetUserAttributesTheEvent(t *testing.T) {
	_, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) { SetUser(w, "12") }, "/admin/stacks/1")
	if got := requestEvent(t, rec).UserID; got != "12" {
		t.Errorf("user_id = %q, want 12", got)
	}
}

func TestRecoverTurnsAPanicIntoA500AndEvents(t *testing.T) {
	rw, rec := serveTelemetry(t, func(http.ResponseWriter, *http.Request) { panic("assignment to entry in nil map") }, "/admin/stacks/1")

	if rw.Code != http.StatusInternalServerError || !strings.Contains(rw.Body.String(), "internal server error") {
		t.Errorf("response = %d %q, want a 500 envelope", rw.Code, rw.Body.String())
	}
	e := requestEvent(t, rec)
	if d := e.Data.(map[string]any); d["status_code"] != http.StatusInternalServerError || !strings.Contains(d["error"].(string), "nil map") {
		t.Errorf("request event = %v", d)
	}
}

func TestHealthcheckIsNotRecorded(t *testing.T) {
	_, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {}, "/healthcheck")
	if n := len(rec.Named("http.request.end")); n != 0 {
		t.Errorf("healthcheck produced %d events; probes would drown out real traffic", n)
	}
}

// captureStdout sends the logger's stdout lines to a buffer for the test.
func captureStdout(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(func() { logger.SetOutput(os.Stdout) })
	return &buf
}

func TestDeployTokenNeverReachesStdoutOrTheEvent(t *testing.T) {
	const token = "dtk_0123456789abcdef"
	out := captureStdout(t)
	_, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {
		responder.SendError(w, http.StatusUnauthorized, "invalid deploy token")
	}, "/api/deploy/"+token)

	if strings.Contains(out.String(), token) {
		t.Errorf("stdout leaked the deploy token:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/api/deploy/{token}") {
		t.Errorf("stdout request line should carry the route template:\n%s", out.String())
	}
	b, _ := json.Marshal(requestEvent(t, rec))
	if strings.Contains(string(b), token) {
		t.Errorf("event leaked the deploy token: %s", b)
	}
}

func TestUpstreamFailureIsAWarningWithAStaticMessage(t *testing.T) {
	rw, rec := serveTelemetry(t, func(w http.ResponseWriter, _ *http.Request) {
		responder.SendUpstreamError(w, http.StatusBadGateway, "registry connection failed", errors.New("dial tcp: lookup registry.example.com: no such host"))
	}, "/admin/stacks/1")

	e := requestEvent(t, rec)
	d := e.Data.(map[string]any)
	if e.Level != monitor.LevelWarn || d["status_code"] != http.StatusBadGateway {
		t.Errorf("level=%q status=%v, want a warning 502", e.Level, d["status_code"])
	}
	if d["error_message"] != "registry connection failed" || !strings.Contains(d["error"].(string), "no such host") {
		t.Errorf("data = %v", d)
	}
	// The person fixing their registry still sees why.
	if !strings.Contains(rw.Body.String(), "no such host") {
		t.Errorf("body lost the upstream reason: %s", rw.Body.String())
	}
}

// serveUnmatched runs a request through a router shaped like main.go's: global
// middleware, an authenticated-style subrouter, and wrapped unmatched handlers.
func serveUnmatched(t *testing.T, method, target string) (*httptest.ResponseRecorder, *monitor.Recorder, string) {
	t.Helper()
	out := captureStdout(t)
	rec := monitor.StartRecording()
	t.Cleanup(rec.Stop)

	global := []mux.MiddlewareFunc{RequestIDMiddleware, LoggingMiddleware, RecoverMiddleware, MuxHeaderMiddleware}
	r := mux.NewRouter()
	r.Use(global...)
	r.HandleFunc("/api/deploy/{token}", func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodPost)
	r.HandleFunc("/api/automations/{token}", func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodPost)
	admin := r.PathPrefix("/admin").Subrouter()
	admin.HandleFunc("/stacks/{id}", func(http.ResponseWriter, *http.Request) {}).Methods(http.MethodGet)
	r.NotFoundHandler = Wrap(UnmatchedHandler(http.StatusNotFound), global...)
	r.MethodNotAllowedHandler = Wrap(UnmatchedHandler(http.StatusMethodNotAllowed), global...)

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, httptest.NewRequest(method, target, nil))
	return rw, rec, out.String()
}

func TestUnmatchedRoutesProduceAnEvent(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		status int
	}{
		{"unknown path", http.MethodGet, "/nope/secret-looking-segment", http.StatusNotFound},
		{"unknown path under a subrouter", http.MethodGet, "/admin/nope/secret-looking-segment", http.StatusNotFound},
		{"wrong method", http.MethodGet, "/api/deploy/secret-looking-segment", http.StatusMethodNotAllowed},
		{"wrong method under a subrouter", http.MethodDelete, "/admin/stacks/1", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw, rec, out := serveUnmatched(t, tt.method, tt.target)
			if rw.Code != tt.status {
				t.Fatalf("status = %d, want %d", rw.Code, tt.status)
			}
			if rw.Header().Get("X-Request-ID") == "" {
				t.Error("unmatched response missing X-Request-ID; the middleware chain did not run")
			}
			e := requestEvent(t, rec)
			d := e.Data.(map[string]any)
			if d["status_code"] != tt.status || e.Level != monitor.LevelWarn {
				t.Errorf("level=%q data=%v", e.Level, d)
			}
			if p, _ := d["path"].(string); p != UnmatchedRoute && strings.Contains(p, "secret-looking-segment") {
				t.Errorf("path = %q; an unmatched path must never be the grouping key", p)
			}
			if strings.Contains(out, "secret-looking-segment") {
				t.Errorf("stdout carried the raw path:\n%s", out)
			}
		})
	}
}

// An unmatched request has no route variables, so nothing tells the redactor
// which segment is the credential: the raw path must not be recorded at all.
func TestUnmatchedCredentialPathsNeverLeakTheToken(t *testing.T) {
	const token = "SECRETTOKEN0123456789"
	tests := []struct {
		name   string
		method string
		target string
		status int
	}{
		{"deploy, wrong method", http.MethodGet, "/api/deploy/" + token, http.StatusMethodNotAllowed},
		{"deploy, extra segment", http.MethodPost, "/api/deploy/" + token + "/x", http.StatusNotFound},
		{"automations, wrong method", http.MethodGet, "/api/automations/" + token, http.StatusMethodNotAllowed},
		{"automations, extra segment", http.MethodPost, "/api/automations/" + token + "/x", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw, rec, out := serveUnmatched(t, tt.method, tt.target)
			if rw.Code != tt.status {
				t.Fatalf("status = %d, want %d", rw.Code, tt.status)
			}
			if strings.Contains(out, token) {
				t.Errorf("stdout leaked the token:\n%s", out)
			}
			e := requestEvent(t, rec)
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), token) {
				t.Errorf("event leaked the token: %s", b)
			}
			if d := e.Data.(map[string]any); d["request_path"] != UnmatchedRoute {
				t.Errorf("request_path = %v, want %q", d["request_path"], UnmatchedRoute)
			}
		})
	}
}

func TestRedactedPathDropsUnmatchedPaths(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/deploy/SECRETTOKEN", nil)
	if got := redactedPath(r); got != UnmatchedRoute {
		t.Errorf("redactedPath = %q, want %q (panic path would leak the token)", got, UnmatchedRoute)
	}
}
