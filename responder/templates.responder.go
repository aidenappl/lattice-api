package responder

import (
	"errors"
	"net/http"
)

// notFounder is satisfied by errors that represent a missing resource (e.g.
// query.ErrNotFound). Detecting it via a local interface keeps responder a leaf
// package — it does not import query (and thus not db/env).
type notFounder interface {
	NotFound() bool
}

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
	// A missing row is a 404, not a server error. Getters return query.ErrNotFound
	// on sql.ErrNoRows, so every handler that funnels its query error through here
	// gets consistent not-found handling for free.
	var nf notFounder
	if errors.As(err, &nf) && nf.NotFound() {
		SendError(w, http.StatusNotFound, "resource not found")
		return
	}
	SendError(w, http.StatusInternalServerError, message, err)
}

func NotFound(w http.ResponseWriter) {
	SendError(w, http.StatusNotFound, "resource not found")
}
