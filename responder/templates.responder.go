package responder

import "net/http"

func BadBody(w http.ResponseWriter, err error) {
	if err == nil {
		SendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	SendError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
}

func MissingBodyFields(w http.ResponseWriter, message string) {
	SendError(w, http.StatusBadRequest, "Missing required body fields: "+message)
}

func QueryError(w http.ResponseWriter, err error, message string) {
	SendError(w, http.StatusInternalServerError, message, err)
}

func NotFound(w http.ResponseWriter) {
	SendError(w, http.StatusNotFound, "resource not found")
}
