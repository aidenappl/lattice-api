package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	monitor "github.com/aidenappl/go-monitor"
)

func TestRequestIDMiddlewareCorrelation(t *testing.T) {
	const (
		validReq    = "0123456789abcdef"
		validUUID   = "11111111-2222-4333-8444-555555555555"
		tpTrace     = "4bf92f3577b34da6a3ce929d0e0e4736"
		validTP     = "00-" + tpTrace + "-00f067aa0ba902b7-01"
		validXTrace = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)

	tests := []struct {
		name      string
		headers   map[string]string
		wantReq   string // "" = must be freshly minted
		wantTrace string // "" = must be freshly minted
	}{
		{name: "missing ids are minted", headers: nil},
		{name: "valid hex X-Request-ID is kept", headers: map[string]string{"X-Request-ID": validReq}, wantReq: validReq},
		{name: "valid uuid X-Request-ID is kept", headers: map[string]string{"X-Request-ID": validUUID}, wantReq: validUUID},
		{name: "invalid X-Request-ID is replaced", headers: map[string]string{"X-Request-ID": "not a valid id!"}},
		{name: "too short X-Request-ID is replaced", headers: map[string]string{"X-Request-ID": "abc"}},
		{name: "valid traceparent supplies trace", headers: map[string]string{"traceparent": validTP}, wantTrace: tpTrace},
		{name: "traceparent wins over X-Trace-ID", headers: map[string]string{"traceparent": validTP, "X-Trace-ID": validXTrace}, wantTrace: tpTrace},
		{name: "invalid traceparent falls back to X-Trace-ID", headers: map[string]string{"traceparent": "00-xyz-00f067aa0ba902b7-01", "X-Trace-ID": validXTrace}, wantTrace: validXTrace},
		{name: "all-zero traceparent trace-id is rejected", headers: map[string]string{"traceparent": "00-00000000000000000000000000000000-00f067aa0ba902b7-01"}},
		{name: "uppercase traceparent is rejected", headers: map[string]string{"traceparent": "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"}},
		{name: "valid X-Trace-ID is kept", headers: map[string]string{"X-Trace-ID": validXTrace}, wantTrace: validXTrace},
		{name: "invalid X-Trace-ID is replaced", headers: map[string]string{"X-Trace-ID": "<script>"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ctxReq, ctxTrace, legacyReq string
			h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctxReq = monitor.RequestID(r.Context())
				ctxTrace = monitor.TraceID(r.Context())
				legacyReq = GetRequestID(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rw := httptest.NewRecorder()
			h.ServeHTTP(rw, req)

			gotReq := rw.Header().Get("X-Request-ID")
			gotTrace := rw.Header().Get("X-Trace-ID")

			if tt.wantReq != "" {
				if gotReq != tt.wantReq {
					t.Errorf("X-Request-ID = %q, want %q", gotReq, tt.wantReq)
				}
			} else if !uuidRegex.MatchString(gotReq) {
				t.Errorf("minted X-Request-ID %q is not a UUID", gotReq)
			}
			if tt.wantTrace != "" {
				if gotTrace != tt.wantTrace {
					t.Errorf("X-Trace-ID = %q, want %q", gotTrace, tt.wantTrace)
				}
			} else if !uuidRegex.MatchString(gotTrace) {
				t.Errorf("minted X-Trace-ID %q is not a UUID", gotTrace)
			}
			if !monitor.ValidCorrelationID(gotReq) || !monitor.ValidCorrelationID(gotTrace) {
				t.Errorf("ids must pass ValidCorrelationID: %q %q", gotReq, gotTrace)
			}
			if ctxReq != gotReq || legacyReq != gotReq {
				t.Errorf("ctx request id %q / legacy %q, want header %q", ctxReq, legacyReq, gotReq)
			}
			if ctxTrace != gotTrace {
				t.Errorf("ctx trace id %q, want header %q", ctxTrace, gotTrace)
			}
		})
	}
}

// A header sent by the browser reaches http.request.end.
func TestRequestEndCarriesInboundIDs(t *testing.T) {
	rec := monitor.StartRecording()
	t.Cleanup(rec.Stop)

	h := RequestIDMiddleware(LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	req := httptest.NewRequest(http.MethodGet, "/admin/stacks/1", nil)
	req.Header.Set("X-Request-ID", "11111111-2222-4333-8444-555555555555")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	e := requestEvent(t, rec)
	if e.RequestID != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("request_id = %q", e.RequestID)
	}
	if e.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id = %q", e.TraceID)
	}
}
