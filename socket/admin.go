package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/structs"
	"github.com/gorilla/websocket"
)

// AdminSession represents a connected admin frontend client.
type AdminSession struct {
	ID          string
	Conn        *websocket.Conn
	Send        chan []byte
	ConnectedAt time.Time

	cancel context.CancelFunc
	once   sync.Once
}

func (s *AdminSession) Close() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.Send)
		_ = s.Conn.Close()
	})
}

const MaxAdminSessions = 50

// AdminHub broadcasts events to connected admin frontend clients.
type AdminHub struct {
	mu       sync.RWMutex
	sessions map[string]*AdminSession
	counter  int
}

func NewAdminHub() *AdminHub {
	return &AdminHub{
		sessions: make(map[string]*AdminSession),
	}
}

func (h *AdminHub) Register(session *AdminSession) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) >= MaxAdminSessions {
		logger.Warn("socket", "admin connection rejected, max sessions reached", logger.F{"max": MaxAdminSessions})
		return fmt.Errorf("maximum admin connections reached (%d)", MaxAdminSessions)
	}
	h.sessions[session.ID] = session
	return nil
}

func (h *AdminHub) Unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[id]; ok {
		delete(h.sessions, id)
		s.Close()
	}
}

func (h *AdminHub) removeIfMatch(session *AdminSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[session.ID]; ok && s == session {
		delete(h.sessions, session.ID)
	}
}

// Broadcast sends a message to all connected admin clients.
func (h *AdminHub) Broadcast(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, session := range h.sessions {
		select {
		case session.Send <- payload:
		default:
			logger.Warn("socket", "admin broadcast queue full", logger.F{"session_id": session.ID})
		}
	}
}

// BroadcastJSON marshals v and broadcasts to all admin clients.
func (h *AdminHub) BroadcastJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.Error("socket", "failed to marshal admin broadcast", logger.F{"error": err})
		return
	}
	h.Broadcast(b)
}

// AdminHandler handles WebSocket connections from admin frontend clients.
type AdminHandler struct {
	Hub       *AdminHub
	Upgrader  websocket.Upgrader
	OnMessage func(session *AdminSession, msg IncomingMessage)

	// AuthFunc validates the request and returns the authenticated user.
	// If nil, all connections are rejected.
	AuthFunc func(r *http.Request) (*structs.User, bool)
}

func NewAdminHandler(hub *AdminHub) *AdminHandler {
	if hub == nil {
		hub = NewAdminHub()
	}

	return &AdminHandler{
		Hub: hub,
		Upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     CheckAllowedOrigin,
		},
	}
}

func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.AuthFunc == nil {
		http.Error(w, "auth not configured", http.StatusInternalServerError)
		return
	}

	if _, ok := h.AuthFunc(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := h.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("socket", "admin upgrade failed", logger.F{"error": err})
		return
	}

	h.Hub.mu.Lock()
	h.Hub.counter++
	id := fmt.Sprintf("admin-%d", h.Hub.counter)
	h.Hub.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	session := &AdminSession{
		ID:          id,
		Conn:        conn,
		Send:        make(chan []byte, sendBufferSize),
		ConnectedAt: time.Now().UTC(),
		cancel:      cancel,
	}

	if err := h.Hub.Register(session); err != nil {
		logger.Warn("socket", "admin connection rejected", logger.F{"session_id": id, "error": err})
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "max connections reached"),
			time.Now().Add(writeWait),
		)
		conn.Close()
		cancel()
		return
	}

	go h.writePump(ctx, session)
	go h.readPump(ctx, session)

	go func() {
		<-ctx.Done()
		h.Hub.removeIfMatch(session)
	}()
}

func (h *AdminHandler) writePump(ctx context.Context, session *AdminSession) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer session.Close()

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-session.Send:
			_ = session.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				return
			}
			if err := session.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			_ = session.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := session.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *AdminHandler) readPump(ctx context.Context, session *AdminSession) {
	defer session.Close()

	session.Conn.SetReadLimit(maxMessageSize)
	_ = session.Conn.SetReadDeadline(time.Now().Add(pongWait))
	session.Conn.SetPongHandler(func(string) error {
		_ = session.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, payload, err := session.Conn.ReadMessage()
		if err != nil {
			return
		}

		if h.OnMessage != nil {
			var msg IncomingMessage
			msg.Raw = json.RawMessage(payload)
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			h.OnMessage(session, msg)
		}
	}
}
