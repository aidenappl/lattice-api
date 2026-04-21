package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// AllowedOrigins holds the configured allowed origins for WebSocket connections.
// Must be set before handlers are used.
var AllowedOrigins []string

// CheckAllowedOrigin validates that the request's Origin header matches one of the allowed origins.
// If no origins are configured, all origins are rejected. If the request has no Origin header,
// it is allowed (same-origin or non-browser clients).
func CheckAllowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header means same-origin or non-browser client — allow.
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}

	for _, allowed := range AllowedOrigins {
		allowedURL, err := url.Parse(allowed)
		if err != nil {
			continue
		}
		if originURL.Scheme == allowedURL.Scheme && originURL.Host == allowedURL.Host {
			return true
		}
	}
	return false
}

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 64 * 1024
	sendBufferSize = 64
)

// WorkerHandler handles WebSocket connections from workers.
type WorkerHandler struct {
	Hub          *WorkerHub
	Upgrader     websocket.Upgrader
	OnConnect    func(session *WorkerSession)
	OnDisconnect func(session *WorkerSession, err error)
	OnMessage    func(session *WorkerSession, msg IncomingMessage)

	// AuthFunc validates the worker token and returns the worker ID.
	// If nil, all connections are rejected.
	AuthFunc func(r *http.Request) (int, bool)
}

func NewWorkerHandler(hub *WorkerHub) *WorkerHandler {
	if hub == nil {
		hub = NewWorkerHub()
	}

	return &WorkerHandler{
		Hub: hub,
		Upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     CheckAllowedOrigin,
		},
	}
}

func (h *WorkerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.AuthFunc == nil {
		http.Error(w, "auth not configured", http.StatusInternalServerError)
		return
	}

	workerID, ok := h.AuthFunc(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := h.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("socket: upgrade failed for worker=%d: %v", workerID, err)
		return
	}

	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))

	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	session := &WorkerSession{
		WorkerID:    workerID,
		Conn:        conn,
		LastSeenAt:  time.Now().UTC(),
		ConnectedAt: time.Now().UTC(),
		Send:        make(chan []byte, sendBufferSize),
		cancel:      cancel,
	}

	h.Hub.Register(session)

	if h.OnConnect != nil {
		h.OnConnect(session)
	}

	// Send connected acknowledgment
	hello := Envelope{
		Type:     MsgConnected,
		WorkerID: fmt.Sprintf("%d", workerID),
		Payload: map[string]any{
			"server_time": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := h.Hub.SendJSONToWorker(workerID, hello); err != nil {
		log.Printf("socket: failed to queue connected message for worker=%d: %v", workerID, err)
	}

	go h.writePump(ctx, session)
	go h.readPump(ctx, session)

	go func() {
		<-ctx.Done()
		if h.OnDisconnect != nil {
			h.OnDisconnect(session, nil)
		}
		h.Hub.removeIfMatch(session)
	}()
}

func (h *WorkerHandler) writePump(ctx context.Context, session *WorkerSession) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer session.Close()

	for {
		select {
		case <-ctx.Done():
			_ = session.Conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "closing"),
				time.Now().Add(writeWait),
			)
			return

		case msg, ok := <-session.Send:
			_ = session.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = session.Conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "send channel closed"),
					time.Now().Add(writeWait),
				)
				return
			}

			if err := session.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("socket: write failed for worker=%d: %v", session.WorkerID, err)
				return
			}

		case <-ticker.C:
			_ = session.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := session.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("socket: ping failed for worker=%d: %v", session.WorkerID, err)
				return
			}
		}
	}
}

func (h *WorkerHandler) readPump(ctx context.Context, session *WorkerSession) {
	defer session.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		messageType, payload, err := session.Conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("socket: read error for worker=%d: %v", session.WorkerID, err)
			}
			return
		}

		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}

		session.LastSeenAt = time.Now().UTC()
		_ = session.Conn.SetReadDeadline(time.Now().Add(pongWait))

		var msg IncomingMessage
		msg.Raw = json.RawMessage(payload)
		if err := json.Unmarshal(payload, &msg); err != nil {
			log.Printf("socket: invalid json from worker=%d: %v", session.WorkerID, err)
			continue
		}

		if h.OnMessage != nil {
			h.OnMessage(session, msg)
		}
	}
}
