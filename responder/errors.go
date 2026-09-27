package responder

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/logger"
)

type ErrorResponse struct {
	Success      bool   `json:"success"`
	Error        any    `json:"error"`
	ErrorMessage string `json:"error_message"`
	ErrorCode    int    `json:"error_code"`
}

// failureRecorder is implemented by the logging middleware's response writer.
// Handing it the reason a request failed puts that reason on the request's
// Monitor event, where it is grouped into an issue by route and cause. It is an
// interface, not an import, so responder stays a leaf package.
type failureRecorder interface {
	RecordFailure(message string, err error, code int)
}

// upstreamFailureRecorder is failureRecorder for a failure caused by a
// third-party service the user configured; the request is then recorded as a
// warning rather than an issue.
type upstreamFailureRecorder interface {
	RecordUpstreamFailure(message string, err error, code int)
}

// recordFailure finds the failureRecorder under any wrapping writers. The
// internal error goes to Monitor even for a 5xx, where the client only ever
// sees "internal server error". It reports whether a recorder took it.
func recordFailure(w http.ResponseWriter, message string, code int, err []error, upstream bool) bool {
	var cause error
	if len(err) > 0 {
		cause = err[0]
	}
	for w != nil {
		if upstream {
			if ur, ok := w.(upstreamFailureRecorder); ok {
				ur.RecordUpstreamFailure(message, cause, code)
				return true
			}
		}
		if fr, ok := w.(failureRecorder); ok {
			fr.RecordFailure(message, cause, code)
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
	return false
}

// stdout prints to stdout only, never to Monitor.
var stdout = slog.New(logger.StdoutHandler())

// logFailure logs an error response once. Behind the request middleware the
// failure is already on the request's Monitor event, so the line goes to
// stdout only; without it (a writer nothing wraps), it is the only record and
// goes to Monitor too.
func logFailure(recorded bool, status int, message string, code int, err []error, upstream bool) {
	fields := logger.F{"status": status, "error_message": message, "error_code": code}
	if len(err) > 0 && err[0] != nil {
		fields["error"] = err[0]
	}
	if recorded {
		level := slog.LevelWarn
		if status >= 500 && !upstream {
			level = slog.LevelError
		}
		attrs := []any{"component", "http"}
		for _, k := range []string{"status", "error_message", "error_code", "error"} {
			if v, ok := fields[k]; ok {
				if e, isErr := v.(error); isErr {
					v = e.Error()
				}
				attrs = append(attrs, k, v)
			}
		}
		stdout.Log(context.Background(), level, "error response", attrs...)
		return
	}
	if status >= 500 && !upstream {
		logger.ErrorCtx(context.Background(), "http", "error response", fields)
		return
	}
	logger.WarnCtx(context.Background(), "http", "error response", fields)
}

func SendError(w http.ResponseWriter, status int, errMessage string, err ...error) {
	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: strings.ToLower(errMessage),
		ErrorCode:    1000,
	}
	recorded := recordFailure(w, errResp.ErrorMessage, errResp.ErrorCode, err, false)
	logFailure(recorded, status, errResp.ErrorMessage, errResp.ErrorCode, err, false)

	// For 5xx errors, never expose the raw error to clients.
	// For 4xx errors, the message is user-facing and safe to include.
	if len(err) > 0 && err[0] != nil {
		if status >= 500 {
			errResp.Error = "internal server error"
		} else {
			errResp.Error = err[0].Error()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp)
}

func SendErrorWithCode(w http.ResponseWriter, status int, errMessage string, code int, err ...error) {
	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: strings.ToLower(errMessage),
		ErrorCode:    code,
	}
	recorded := recordFailure(w, errResp.ErrorMessage, errResp.ErrorCode, err, false)
	logFailure(recorded, status, errResp.ErrorMessage, errResp.ErrorCode, err, false)

	if len(err) > 0 && err[0] != nil {
		if status >= 500 {
			errResp.Error = "internal server error"
		} else {
			errResp.Error = err[0].Error()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp)
}

// SendUpstreamError reports that a third-party service the user configured —
// a container registry, say — failed or refused the request. errMessage must
// be static: it is what Monitor groups the failure by, while err (the
// upstream's own text) rides in the event's error field. The request is
// recorded as a warning, since nothing in this service is broken.
//
// The client still sees the upstream reason, in both error and
// error_message, because that is what the person needs to fix it.
func SendUpstreamError(w http.ResponseWriter, status int, errMessage string, err error) {
	message := strings.ToLower(errMessage)
	errs := []error{err}
	recorded := recordFailure(w, message, 1000, errs, true)
	logFailure(recorded, status, message, 1000, errs, true)

	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: message,
		ErrorCode:    1000,
	}
	if err != nil {
		errResp.Error = err.Error()
		errResp.ErrorMessage = message + ": " + err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp)
}
