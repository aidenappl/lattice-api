package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrWorkerNotConnected = errors.New("worker is not connected")
	ErrSendQueueFull      = errors.New("worker send queue is full")
)

// WorkerSession represents a single connected worker.
type WorkerSession struct {
	WorkerID    int
	Conn        *websocket.Conn
	LastSeenAt  time.Time
	ConnectedAt time.Time
	Send        chan []byte

	cancel context.CancelFunc
	once   sync.Once
}

func (s *WorkerSession) Close() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.Send)
		_ = s.Conn.Close()
	})
}

// WorkerHub manages all connected worker WebSocket sessions.
type WorkerHub struct {
	mu       sync.RWMutex
	sessions map[int]*WorkerSession
}

func NewWorkerHub() *WorkerHub {
	return &WorkerHub{
		sessions: make(map[int]*WorkerSession),
	}
}

func (h *WorkerHub) Register(session *WorkerSession) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if old, ok := h.sessions[session.WorkerID]; ok {
		old.Close()
	}

	h.sessions[session.WorkerID] = session
	log.Printf("socket: worker=%d registered (total=%d)", session.WorkerID, len(h.sessions))
}

func (h *WorkerHub) Unregister(workerID int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if s, ok := h.sessions[workerID]; ok {
		delete(h.sessions, workerID)
		s.Close()
		log.Printf("socket: worker=%d unregistered (total=%d)", workerID, len(h.sessions))
	}
}

func (h *WorkerHub) removeIfMatch(session *WorkerSession) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if s, ok := h.sessions[session.WorkerID]; ok && s == session {
		delete(h.sessions, session.WorkerID)
	}
}

func (h *WorkerHub) Get(workerID int) (*WorkerSession, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	s, ok := h.sessions[workerID]
	return s, ok
}

func (h *WorkerHub) IsConnected(workerID int) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	_, ok := h.sessions[workerID]
	return ok
}

func (h *WorkerHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.sessions)
}

func (h *WorkerHub) ListConnectedIDs() []int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	ids := make([]int, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

func (h *WorkerHub) SendToWorker(workerID int, payload []byte) error {
	h.mu.RLock()
	session, ok := h.sessions[workerID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %d", ErrWorkerNotConnected, workerID)
	}

	select {
	case session.Send <- payload:
		return nil
	default:
		return fmt.Errorf("%w: %d", ErrSendQueueFull, workerID)
	}
}

func (h *WorkerHub) SendJSONToWorker(workerID int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	return h.SendToWorker(workerID, b)
}

func (h *WorkerHub) BroadcastAll(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, session := range h.sessions {
		select {
		case session.Send <- payload:
		default:
			log.Printf("socket: broadcast queue full for worker=%d", session.WorkerID)
		}
	}
}
