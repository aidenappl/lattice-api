package socket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// PROXY_READ_TIMEOUT is nginx's default proxy_read_timeout, which NPM applies
// to lattice-api.appleby.cloud (no override in its advanced config).
const PROXY_READ_TIMEOUT = 60 * time.Second

func TestKeepaliveFitsUnderTheProxyTimeout(t *testing.T) {
	if pingPeriod > PROXY_READ_TIMEOUT/2 {
		t.Errorf("pingPeriod = %v, want at most half the proxy's %v so one late ping cannot idle the connection out", pingPeriod, PROXY_READ_TIMEOUT)
	}
	if pongWait <= pingPeriod {
		t.Errorf("pongWait = %v must exceed pingPeriod = %v, or every connection times out between pings", pongWait, pingPeriod)
	}
}

func TestWorkerHubCloseAllSendsGoingAway(t *testing.T) {
	hub := NewWorkerHub()
	h := NewWorkerHandler(hub)
	h.AuthFunc = func(*http.Request) (int, bool) { return 3, true }
	h.OnMessage = func(*WorkerSession, IncomingMessage) {}
	connected := make(chan struct{}, 1)
	h.OnConnect = func(*WorkerSession) { connected <- struct{}{} }

	srv := httptest.NewServer(h)
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never connected")
	}

	hub.CloseAll(websocket.CloseGoingAway, "server shutting down")

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
		t.Fatalf("read after CloseAll = %v, want a 1001 close frame (not a reset)", err)
	}
	if hub.Count() != 0 {
		t.Fatalf("hub still holds %d sessions after CloseAll", hub.Count())
	}
}
