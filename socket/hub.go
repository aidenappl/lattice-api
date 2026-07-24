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

const MaxWorkerSessions = 500

var (
	ErrWorkerNotConnected = errors.New("worker is not connected")
	ErrSendQueueFull      = errors.New("worker send queue is full")
	ErrMaxConnections     = errors.New("maximum connections reached")
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
	// done is closed exactly once in Close(). Senders select on it so they
	// never block on (or send to) a session that is shutting down. Send is
	// never closed — closing it while a concurrent deploy-ping/heartbeat is
	// mid-send would panic ("send on closed channel") and crash the process.
	done           chan struct{}
	DisconnectOnce sync.Once
}

func (s *WorkerSession) Close() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.done != nil {
			close(s.done)
		}
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

func (h *WorkerHub) Register(session *WorkerSession) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Allow re-registration of existing worker (replaces old session)
	if old, ok := h.sessions[session.WorkerID]; ok {
		old.Close()
	} else if len(h.sessions) >= MaxWorkerSessions {
		log.Printf("socket: worker=%d rejected, max connections reached (%d)", session.WorkerID, MaxWorkerSessions)
		return ErrMaxConnections
	}

	h.sessions[session.WorkerID] = session
	log.Printf("socket: worker=%d registered (total=%d)", session.WorkerID, len(h.sessions))
	return nil
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

func (h *WorkerHub) SendToWorker(workerID int, payload []byte) (err error) {
	// Defense-in-depth: a send can never panic now that Send is never closed,
	// but recover here so a hub bug can never crash the whole process.
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%w: %d (recovered: %v)", ErrWorkerNotConnected, workerID, rec)
		}
	}()

	h.mu.RLock()
	session, ok := h.sessions[workerID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %d", ErrWorkerNotConnected, workerID)
	}

	select {
	case session.Send <- payload:
		return nil
	case <-session.done:
		return fmt.Errorf("%w: %d", ErrWorkerNotConnected, workerID)
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
		case <-session.done:
			// session is shutting down — skip it
		default:
			log.Printf("socket: broadcast queue full for worker=%d", session.WorkerID)
		}
	}
}
