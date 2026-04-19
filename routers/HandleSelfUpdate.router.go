package routers

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	extraEnv, cleanup, err := registryAuthEnv()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to prepare registry credentials: %v", err))
		return
	}
	defer cleanup()

	// Pull latest image synchronously so we can report failures.
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull", service)
	pullCmd.Env = append(os.Environ(), extraEnv...)
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
		cmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", service)
		cmd.Env = append(os.Environ(), extraEnv...)
		time.Sleep(2 * time.Second)
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

	extraEnv, cleanup, err := registryAuthEnv()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to prepare registry credentials: %v", err))
		return
	}
	defer cleanup()

	// Pull latest image
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull", service)
	pullCmd.Env = append(os.Environ(), extraEnv...)
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to pull Web image: %v — %s", err, string(pullOut)))
		return
	}

	// Recreate the web container — API stays running so we can respond.
	upCmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", service)
	upCmd.Env = append(os.Environ(), extraEnv...)
	upOut, err := upCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to recreate Web container: %v — %s", err, string(upOut)))
		return
	}

	responder.New(w, map[string]any{
		"service": service,
	}, "Web update triggered successfully")
}

// registryAuthEnv writes a temporary Docker config.json with registry credentials
// and returns a DOCKER_CONFIG env var pointing to it, plus a cleanup function.
// If credentials are not configured it is a no-op (empty env slice, no-op cleanup).
func registryAuthEnv() (extraEnv []string, cleanup func(), err error) {
	cleanup = func() {}
	if env.RegistryURL == "" || env.RegistryUsername == "" || env.RegistryPassword == "" {
		return
	}

	auth := base64.StdEncoding.EncodeToString([]byte(env.RegistryUsername + ":" + env.RegistryPassword))
	configJSON := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, env.RegistryURL, auth)

	tmpDir, err := os.MkdirTemp("", "lattice-docker-config-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err = os.WriteFile(filepath.Join(tmpDir, "config.json"), []byte(configJSON), 0600); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, func() {}, fmt.Errorf("failed to write docker config: %w", err)
	}

	extraEnv = []string{"DOCKER_CONFIG=" + tmpDir}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }
	return
}
