package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
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
