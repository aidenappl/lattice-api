package socket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/logger"
	"github.com/gorilla/websocket"
)

// A panic in OnMessage runs on the read pump's goroutine, where nothing else can
// recover it: uncontained, it kills the process and every worker connection.
// It must cost only the message that caused it — and the disconnect that
// follows must say why the connection ended.
func TestPanickingHandlerIsContainedAndCloseCauseReachesOnDisconnect(t *testing.T) {
	var mu sync.Mutex
	var panics []logger.PanicRecord
	logger.SetPanicSink(func(_ context.Context, p logger.PanicRecord) {
		mu.Lock()
		panics = append(panics, p)
		mu.Unlock()
	})
	t.Cleanup(func() { logger.SetPanicSink(nil) })

	h := NewWorkerHandler(nil)
	h.AuthFunc = func(*http.Request) (int, bool) { return 7, true }
	handled := make(chan string, 4)
	h.OnMessage = func(_ *WorkerSession, msg IncomingMessage) {
		if msg.Type == "boom" {
			panic("handler bug")
		}
		handled <- msg.Type
	}
	disconnected := make(chan error, 1)
	h.OnDisconnect = func(_ *WorkerSession, err error) { disconnected <- err }

	srv := httptest.NewServer(h)
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if err := conn.WriteJSON(map[string]any{"type": "boom"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"type": MsgHeartbeat}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-handled:
		if got != MsgHeartbeat {
			t.Fatalf("handled %q, want %q", got, MsgHeartbeat)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read pump died with the panicking handler")
	}

	mu.Lock()
	if len(panics) != 1 || panics[0].Goroutine != "socket.worker.on_message" || panics[0].Fields["message_type"] != "boom" || panics[0].Fields["worker_id"] != 7 {
		t.Errorf("panic reports = %+v", panics)
	}
	mu.Unlock()

	// Drop the connection without a close frame, as a crashed runner would.
	_ = conn.UnderlyingConn().Close()

	select {
	case cause := <-disconnected:
		if cause == nil || !strings.HasPrefix(cause.Error(), "read: ") {
			t.Errorf("OnDisconnect cause = %v, want the read error that ended the connection", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnDisconnect was never called")
	}
}

// failureCapture stands in for the logging middleware's writer: a 500 from a
// socket handler must reach RecordFailure like any other handler's.
type failureCapture struct {
	http.ResponseWriter
	message string
	err     error
	code    int
}

func (f *failureCapture) RecordFailure(message string, err error, code int) {
	f.message, f.err, f.code = message, err, code
}

func TestUnconfiguredAuthGoesThroughTheResponder(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{"worker", NewWorkerHandler(nil)},
		{"admin", NewAdminHandler(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			w := &failureCapture{ResponseWriter: rec}
			tt.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if w.message != "auth not configured" || w.err == nil {
				t.Errorf("RecordFailure got message=%q err=%v", w.message, w.err)
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
				t.Errorf("Content-Type = %q, want the responder's JSON", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestMessageType(t *testing.T) {
	tests := []struct {
		payload string
		want    string
	}{
		{`{"type":"worker_heartbeat","payload":{}}`, "worker_heartbeat"},
		{`{"payload":{}}`, ""},
		{`not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.payload, func(t *testing.T) {
			if got := messageType([]byte(tt.payload)); got != tt.want {
				t.Errorf("messageType = %q, want %q", got, tt.want)
			}
		})
	}
}
