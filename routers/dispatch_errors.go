package routers

import (
	"errors"
	"net/http"

	"github.com/aidenappl/lattice-api/responder"
	"github.com/aidenappl/lattice-api/socket"
)

// sendDispatchError answers a command that could not be handed to a worker.
// The message stays static, so Monitor groups it by command, while the reason
// the client has always been shown ("worker is not connected") is kept when it
// is one of the hub's known ones. Anything else stays in err only.
func sendDispatchError(w http.ResponseWriter, command string, err error) {
	responder.SendError(w, http.StatusInternalServerError, dispatchErrorMessage(command, err), err)
}

func dispatchErrorMessage(command string, err error) string {
	msg := "failed to send " + command + " command"
	switch {
	case errors.Is(err, socket.ErrWorkerNotConnected):
		msg += ": " + socket.ErrWorkerNotConnected.Error()
	case errors.Is(err, socket.ErrSendQueueFull):
		msg += ": " + socket.ErrSendQueueFull.Error()
	}
	return msg
}
