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
	log.Println(status, " Error response:", errMessage)
	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: strings.ToLower(errMessage),
		ErrorCode:    1000,
	}
	if len(err) > 0 && err[0] != nil {
		errResp.Error = err[0].Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp)
}

func SendErrorWithCode(w http.ResponseWriter, status int, errMessage string, code int, err ...error) {
	log.Println(status, " Error response:", errMessage)
	errResp := ErrorResponse{
		Success:      false,
		Error:        nil,
		ErrorMessage: strings.ToLower(errMessage),
		ErrorCode:    code,
	}
	if len(err) > 0 && err[0] != nil {
		errResp.Error = err[0].Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errResp)
}
