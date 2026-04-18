package responder

import (
	"encoding/json"
	"net/http"
	"strings"
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

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
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

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
