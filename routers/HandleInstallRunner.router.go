package routers

import "net/http"

// InstallScript is set by main.go from the embedded file.
var InstallScript []byte

func HandleInstallRunner(w http.ResponseWriter, r *http.Request) {
	if len(InstallScript) == 0 {
		http.Error(w, "install script not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Write(InstallScript)
}
