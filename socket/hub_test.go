package socket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A worker that reconnects before the server noticed its old connection die
// replaces that session. The old session's OnDisconnect runs after the new
// session's OnConnect, so it must be identifiable as replaced — otherwise it
// marks a connected worker offline and schedules an alert nothing cancels.
func TestReplacedSessionIsFlaggedOnDisconnect(t *testing.T) {
	h := NewWorkerHandler(nil)
	h.AuthFunc = func(*http.Request) (int, bool) { return 7, true }
	h.OnMessage = func(*WorkerSession, IncomingMessage) {}
	type disconnect struct {
		replaced bool
		cause    error
	}
	disconnected := make(chan disconnect, 2)
	h.OnDisconnect = func(s *WorkerSession, err error) {
		disconnected <- disconnect{replaced: s.Replaced.Load(), cause: err}
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	first, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer first.Close()
	second, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}

	select {
	case d := <-disconnected:
		if !d.replaced {
			t.Fatalf("replaced session disconnected with Replaced=false (cause %v)", d.cause)
		}
		if d.cause == nil || !strings.Contains(d.cause.Error(), "replaced") {
			t.Fatalf("cause = %v, want the replacement cause", d.cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnDisconnect was not called for the replaced session")
	}

	// The surviving session ending on its own is a real disconnect.
	second.Close()
	select {
	case d := <-disconnected:
		if d.replaced {
			t.Fatal("a session that was never replaced reported Replaced=true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnDisconnect was not called for the surviving session")
	}
}
