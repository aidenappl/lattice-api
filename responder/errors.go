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
