package middleware

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	monitor "github.com/aidenappl/go-monitor"
	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type contextKey string

const (
	RequestIDKey contextKey = "request-id"
)

func GetRequestID(ctx context.Context) string {
	if requestID, ok := ctx.Value(RequestIDKey).(string); ok {
		return requestID
	}
	return "unknown"
}

func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := uuid.New().String()
		ctx := context.WithValue(r.Context(), RequestIDKey, requestID)
		// The same id goes on every Monitor event the request produces, so an
		// event and this service's own log lines are joined by one value.
		ctx = monitor.WithRequestID(ctx, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// failure is why a request failed, as reported by responder.SendError.
type failure struct {
	message string
	err     string
	code    int
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int
	hijacked   bool
	failure    *failure
	userID     string
}

func (rw *statusResponseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *statusResponseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Hijack implements http.Hijacker by delegating to the underlying ResponseWriter.
// This is required for WebSocket upgrades — embedding http.ResponseWriter only
// promotes methods defined on that interface; Hijack() must be forwarded explicitly.
func (rw *statusResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	conn, buf, err := hj.Hijack()
	if err == nil {
		rw.hijacked = true
	}
	return conn, buf, err
}

// Unwrap exposes the writer underneath, for http.ResponseController and for
// callers that look for this writer through other wrappers.
func (rw *statusResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// RecordFailure keeps the reason a request failed so it lands on the request's
// Monitor event — the one place it is recorded alongside the request_id, route
// and caller it belongs to. responder.SendError calls it.
func (rw *statusResponseWriter) RecordFailure(message string, err error, code int) {
	f := &failure{message: message, code: code}
	if err != nil {
		f.err = err.Error()
	}
	rw.failure = f
}

// findStatusWriter locates this package's writer under any wrappers.
func findStatusWriter(w http.ResponseWriter) *statusResponseWriter {
	for w != nil {
		if rw, ok := w.(*statusResponseWriter); ok {
			return rw
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// SetUser attributes the request to an authenticated principal on its Monitor
// event. Call it only once authentication has SUCCEEDED: an identity taken from
// an unverified credential would let a caller write any name into the record of
// the system meant to catch them.
func SetUser(w http.ResponseWriter, userID string) {
	if rw := findStatusWriter(w); rw != nil {
		rw.userID = userID
	}
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthcheck" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()

		srw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(srw, r)

		duration := time.Since(start)
		requestID := GetRequestID(r.Context())
		logger.Request(requestID, r.Method, r.URL.Path, srw.statusCode, duration)

		emitRequest(r, srw, duration)
	})
}

// emitRequest sends the request's one Monitor event. 5xx is an error (and so an
// issue, grouped by route and cause), 4xx a warning, everything else info.
func emitRequest(r *http.Request, srw *statusResponseWriter, duration time.Duration) {
	status := srw.statusCode
	if srw.hijacked {
		// gorilla/websocket writes its 101 straight to the hijacked connection,
		// past this writer.
		status = http.StatusSwitchingProtocols
	}
	data := map[string]any{
		"method":         r.Method,
		"path":           routeTemplate(r),
		"request_path":   r.URL.Path,
		"status_code":    status,
		"duration_ms":    duration.Milliseconds(),
		"response_bytes": srw.bytes,
		"client_ip":      getClientIP(r),
		"user_agent":     r.UserAgent(),
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		data["websocket"] = true
	}
	if f := srw.failure; f != nil {
		data["error_message"] = f.message
		if f.err != "" {
			data["error"] = f.err
		}
		if f.code != 0 {
			data["error_code"] = f.code
		}
	}

	level := monitor.LevelInfo
	switch {
	case status >= 500:
		level = monitor.LevelError
	case status >= 400:
		level = monitor.LevelWarn
	}

	ctx := r.Context()
	if srw.userID != "" {
		ctx = monitor.WithUserID(ctx, srw.userID)
	}
	monitor.Emit(ctx, "http.request.end", data, monitor.WithLevel(level))
}

// routeTemplate is the matched route pattern ("/admin/stacks/{id}"). Monitor
// groups issues by it; the raw path would split one failing endpoint into an
// issue per id — and /api/deploy/{token} would put a credential in the grouping
// key.
func routeTemplate(r *http.Request) string {
	if route := mux.CurrentRoute(r); route != nil {
		if t, err := route.GetPathTemplate(); err == nil {
			return t
		}
	}
	return r.URL.Path
}

// RecoverMiddleware turns a panicking handler into a 500 response and a
// panic.recovered event instead of a dropped connection with nothing recorded.
// Register it inside LoggingMiddleware, so the request's own event still fires
// with the 500 it produced.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				// net/http's deliberate abort, not a bug.
				panic(rec)
			}

			rw := findStatusWriter(w)
			logger.PanicContext(r.Context(), "http "+r.Method+" "+routeTemplate(r), rec, logger.F{
				"request_id": GetRequestID(r.Context()),
				"path":       r.URL.Path,
			})
			if rw != nil {
				rw.RecordFailure("internal server error", fmt.Errorf("panic: %v", rec), 1000)
				if rw.hijacked {
					// A WebSocket handler: the connection is no longer HTTP.
					return
				}
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":null,"error_message":"internal server error","error_code":1000}` + "\n"))
		}()
		next.ServeHTTP(w, r)
	})
}

func MuxHeaderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Go")
		next.ServeHTTP(w, r)
	})
}

// SecurityHeadersMiddleware sets standard security headers on every response.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		if env.Environment == "production" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// MaxBodySize limits request body size to prevent memory exhaustion.
// The limit parameter is in bytes. Requests exceeding this limit receive 413.
func MaxBodySize(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip for WebSocket upgrades
			if r.Header.Get("Upgrade") == "websocket" {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
