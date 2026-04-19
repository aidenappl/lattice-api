package routers

import (
	"fmt"
	"net/http"
	"os/exec"

	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleUpdateAPI(w http.ResponseWriter, r *http.Request) {
	handleServiceUpdate(w, env.APIServiceName, "API")
}

func HandleUpdateWeb(w http.ResponseWriter, r *http.Request) {
	handleServiceUpdate(w, env.WebServiceName, "Web")
}

func handleServiceUpdate(w http.ResponseWriter, service string, label string) {
	if env.DockerComposeDir == "" {
		responder.SendError(w, http.StatusBadRequest, "self-update not configured: DOCKER_COMPOSE_DIR is not set")
		return
	}

	// Pull latest image and recreate the service container.
	// docker compose -f <dir>/docker-compose.yml pull <service>
	// docker compose -f <dir>/docker-compose.yml up -d <service>
	composeFile := env.DockerComposeDir + "/docker-compose.yml"

	// Pull latest image
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull", service)
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to pull %s image: %v — %s", label, err, string(pullOut)))
		return
	}

	// Recreate the container in detached mode
	upCmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", service)
	upOut, err := upCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to recreate %s container: %v — %s", label, err, string(upOut)))
		return
	}

	responder.New(w, map[string]any{
		"service": service,
		"pull":    string(pullOut),
		"up":      string(upOut),
	}, fmt.Sprintf("%s update triggered successfully", label))
}
