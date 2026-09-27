package routers

import (
	"errors"
	"net/http"

	"github.com/aidenappl/lattice-api/responder"
)

// InstallScript is set by main.go from the embedded file.
var InstallScript []byte

func HandleInstallRunner(w http.ResponseWriter, r *http.Request) {
	if len(InstallScript) == 0 {
		responder.SendError(w, http.StatusInternalServerError, "install script not found", errors.New("embedded install script is empty"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Write(InstallScript)
}
