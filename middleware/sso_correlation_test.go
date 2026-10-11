package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	ssolib "github.com/aidenappl/go-forta/sso"
	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/logger"
)

// The Correlation hook hands go-forta exactly the ids RequestIDMiddleware put on
// the context through go-monitor.
func TestSSOCorrelationReturnsMonitorIDs(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		traceID   string
	}{
		{"both ids", "11111111-2222-4333-8444-555555555555", "4bf92f3577b34da6a3ce929d0e0e4736"},
		{"request id only", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", ""},
		{"no ids", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.requestID != "" {
				ctx = monitor.WithRequestID(ctx, tt.requestID)
			}
			if tt.traceID != "" {
				ctx = monitor.WithTraceID(ctx, tt.traceID)
			}
			rid, tid := ssoCorrelation(ctx)
			if rid != tt.requestID {
				t.Errorf("request id = %q, want %q", rid, tt.requestID)
			}
			if tid != tt.traceID {
				t.Errorf("trace id = %q, want %q", tid, tt.traceID)
			}
		})
	}
}

func TestCheckpointResultLevel(t *testing.T) {
	cause := errors.New("sso: introspect: connection refused")
	tests := []struct {
		name   string
		result ssolib.CheckpointResult
		cause  error
		want   logger.Level
	}{
		{"confirmed live", ssolib.CheckpointOK, nil, logger.LevelDebug},
		{"grace-window pass", ssolib.CheckpointOK, cause, logger.LevelWarn},
		{"revoked", ssolib.CheckpointRevoked, nil, logger.LevelDebug},
		{"unavailable", ssolib.CheckpointUnavailable, cause, logger.LevelWarn},
		{"unavailable without cause", ssolib.CheckpointUnavailable, nil, logger.LevelWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkpointResultLevel(tt.result, tt.cause); got != tt.want {
				t.Errorf("level = %v, want %v", got, tt.want)
			}
		})
	}
}

// memSessionStore is a single-session in-memory ssolib.SessionStore.
type memSessionStore struct {
	mu   sync.Mutex
	sess *ssolib.Session
}

func (m *memSessionStore) SaveSession(_ context.Context, _ int64, s ssolib.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sess = &s
	return nil
}

func (m *memSessionStore) LoadSession(_ context.Context, _ int64) (*ssolib.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == nil {
		return nil, nil
	}
	s := *m.sess
	return &s, nil
}

func (m *memSessionStore) TouchSession(_ context.Context, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess != nil {
		m.sess.LastCheckedAt = time.Now()
	}
	return nil
}

func (m *memSessionStore) DeleteSession(_ context.Context, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sess = nil
	return nil
}

// End to end: a checkpoint run with the request ctx sends that request's ids to
// the IdP's introspection endpoint, and OnResult sees the result.
func TestSSOCheckpointForwardsRequestIDs(t *testing.T) {
	const (
		rid = "11111111-2222-4333-8444-555555555555"
		tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	)
	var gotRID, gotTID string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRID = r.Header.Get("X-Request-ID")
		gotTID = r.Header.Get("X-Trace-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true}`))
	}))
	t.Cleanup(idp.Close)

	store := &memSessionStore{sess: &ssolib.Session{
		Provider: "test",
		Tokens:   ssolib.TokenSet{RefreshToken: "refresh-token"},
	}}
	var results []ssolib.CheckpointResult
	cp := &ssolib.Checkpointer{
		Sessions: store,
		Providers: func(context.Context, string) (*ssolib.Provider, error) {
			return &ssolib.Provider{
				Slug:          "test",
				ClientID:      "client",
				ClientSecret:  "secret",
				IntrospectURL: idp.URL,
			}, nil
		},
		Correlation: ssoCorrelation,
		LogfCtx:     func(context.Context, string, ...any) {},
		OnResult: func(ctx context.Context, userID int64, r ssolib.CheckpointResult, cause error) {
			results = append(results, r)
			logCheckpointResult(ctx, userID, r, cause)
		},
	}

	ctx := monitor.WithTraceID(monitor.WithRequestID(context.Background(), rid), tid)
	if got := cp.Check(ctx, 1); got != ssolib.CheckpointOK {
		t.Fatalf("Check = %v, want ok", got)
	}
	if gotRID != rid {
		t.Errorf("IdP saw X-Request-ID %q, want %q", gotRID, rid)
	}
	if gotTID != tid {
		t.Errorf("IdP saw X-Trace-ID %q, want %q", gotTID, tid)
	}
	if len(results) != 1 || results[0] != ssolib.CheckpointOK {
		t.Errorf("OnResult saw %v, want [ok]", results)
	}
}
