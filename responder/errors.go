package responder

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
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

// recordFailure finds the failureRecorder under any wrapping writers. The
// internal error goes to Monitor even for a 5xx, where the client only ever
// sees "internal server error".
func recordFailure(w http.ResponseWriter, message string, code int, err []error) {
	var cause error
	if len(err) > 0 {
		cause = err[0]
	}
	for w != nil {
		if fr, ok := w.(failureRecorder); ok {
			fr.RecordFailure(message, cause, code)
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

func SendError(w http.ResponseWriter, status int, errMessage string, err ...error) {
	// Log the full error internally for debugging
	if len(err) > 0 && err[0] != nil {
		log.Printf("%d Error response: %s | internal: %v", status, errMessage, err[0])
	} else {
		log.Printf("%d Error response: %s", status, errMessage)
	}

	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: strings.ToLower(errMessage),
		ErrorCode:    1000,
	}
	recordFailure(w, errResp.ErrorMessage, errResp.ErrorCode, err)

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
	if len(err) > 0 && err[0] != nil {
		log.Printf("%d Error response: %s | internal: %v", status, errMessage, err[0])
	} else {
		log.Printf("%d Error response: %s", status, errMessage)
	}

	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: strings.ToLower(errMessage),
		ErrorCode:    code,
	}
	recordFailure(w, errResp.ErrorMessage, errResp.ErrorCode, err)

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
