package responder

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aidenappl/lattice-api/logger"
)

type ResponseStructure struct {
	Success    bool                `json:"success"`
	Message    string              `json:"message"`
	Pagination *ResponsePagination `json:"pagination,omitempty"`
	Data       interface{}         `json:"data"`
}

type ResponsePagination struct {
	Count    int    `json:"count,omitempty"`
	Next     string `json:"next,omitempty"`
	Previous string `json:"previous,omitempty"`
}

func NewWithCount(w http.ResponseWriter, data interface{}, count int, next, previous string, message ...string) {
	response := ResponseStructure{
		Success: true,
		Data:    data,
		Pagination: &ResponsePagination{
			Count:    count,
			Next:     next,
			Previous: previous,
		},
		Message: "request was successful",
	}

	if len(message) > 0 {
		response.Message = message[0]
	}

	// set message to lowercase
	response.Message = strings.ToLower(response.Message)

	writeJSON(w, http.StatusOK, response)
}

func New(w http.ResponseWriter, data interface{}, message ...string) {
	response := ResponseStructure{
		Success:    true,
		Data:       data,
		Pagination: nil,
		Message:    "request was successful",
	}

	if len(message) > 0 {
		response.Message = message[0]
	}

	// set message to lowercase
	response.Message = strings.ToLower(response.Message)

	writeJSON(w, http.StatusOK, response)
}

func NewCreated(w http.ResponseWriter, data interface{}, message ...string) {
	response := ResponseStructure{
		Success:    true,
		Data:       data,
		Pagination: nil,
		Message:    "request was successful",
	}

	if len(message) > 0 {
		response.Message = message[0]
	}

	response.Message = strings.ToLower(response.Message)

	writeJSON(w, http.StatusCreated, response)
}

// writeJSON encodes v before anything is written, so an encoding failure can
// still become a clean 500. A failed write after that is the client going away
// mid-response: the status and part of the body are already sent, so answering
// with a 500 is impossible and recording one would report a disconnect as a
// server error. It is logged at debug instead.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "failed to encode response", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(b, '\n')); err != nil {
		logger.Debug("http", "response write failed", logger.F{"status": status, "error": err})
	}
}
