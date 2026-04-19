package routers

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/responder"
)

func HandleUpdateAPI(w http.ResponseWriter, r *http.Request) {
	if env.DockerComposeDir == "" {
		responder.SendError(w, http.StatusBadRequest, "self-update not configured: DOCKER_COMPOSE_DIR is not set")
		return
	}

	composeFile := env.DockerComposeDir + "/docker-compose.yml"
	service := env.APIServiceName

	// Log in to the registry if credentials are configured.
	if err := registryLogin(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to authenticate with registry: %v", err))
		return
	}

	// Pull latest image synchronously so we can report failures.
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull", service)
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to pull API image: %v — %s", err, string(pullOut)))
		return
	}

	// Respond immediately — the recreate will kill this container.
	responder.New(w, map[string]any{
		"service": service,
		"status":  "pull complete, restarting",
	}, "API update in progress — container will restart momentarily")

	// Flush the response before we die.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Give the HTTP response time to reach the client, then recreate.
	go func() {
		time.Sleep(2 * time.Second)
		cmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", service)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("API self-update recreate failed: %v — %s", err, string(out))
		}
		// If successful, this process is killed by Docker — we never reach here.
	}()
}

func HandleUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if env.DockerComposeDir == "" {
		responder.SendError(w, http.StatusBadRequest, "self-update not configured: DOCKER_COMPOSE_DIR is not set")
		return
	}

	composeFile := env.DockerComposeDir + "/docker-compose.yml"
	service := env.WebServiceName

	// Log in to the registry if credentials are configured.
	if err := registryLogin(); err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to authenticate with registry: %v", err))
		return
	}

	// Pull latest image
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull", service)
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to pull Web image: %v — %s", err, string(pullOut)))
		return
	}

	// Recreate the web container — API stays running so we can respond.
	upCmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", service)
	upOut, err := upCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to recreate Web container: %v — %s", err, string(upOut)))
		return
	}

	responder.New(w, map[string]any{
		"service": service,
	}, "Web update triggered successfully")
}

// registryLogin runs `docker login` with the configured registry credentials.
// It is a no-op if any of the three registry env vars are unset.
func registryLogin() error {
	if env.RegistryURL == "" || env.RegistryUsername == "" || env.RegistryPassword == "" {
		return nil
	}
	cmd := exec.Command("docker", "login", env.RegistryURL,
		"--username", env.RegistryUsername,
		"--password-stdin",
	)
	cmd.Stdin = strings.NewReader(env.RegistryPassword)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w — %s", err, string(out))
	}
	return nil
}
