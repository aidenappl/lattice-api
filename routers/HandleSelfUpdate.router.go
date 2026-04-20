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

	// Respond immediately before we trigger the recreate.
	responder.New(w, map[string]any{
		"service": service,
		"status":  "pull complete, restarting",
	}, "API update in progress — container will restart momentarily")

	// Flush the response before the container is replaced.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Recreate the API container with the newly-pulled image.
	//
	// We cannot run `docker compose up --force-recreate` directly: that command
	// runs inside this container, and Docker kills all processes in the container
	// (including this goroutine) as part of the stop step — so the create/start
	// steps never execute.
	//
	// Instead we spin up a short-lived sibling container that runs the compose
	// command. It shares the Docker socket and compose directory but lives in a
	// separate process namespace, so it survives this container's death.
	go func() {
		time.Sleep(2 * time.Second)
		upCmd := exec.Command(
			"docker", "run", "--rm", "-d",
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			"-v", env.DockerComposeDir+":"+env.DockerComposeDir,
			"--workdir", env.DockerComposeDir,
			"docker:cli",
			"docker", "compose", "-f", composeFile,
			"up", "-d", "--force-recreate", service,
		)
		upCmd.Env = append(os.Environ(), extraEnv...)
		if out, err := upCmd.CombinedOutput(); err != nil {
			log.Printf("API self-update recreate failed: %v — %s", err, string(out))
		}
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
	upCmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--force-recreate", service)
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

// registryAuthEnv writes registry credentials into /root/.docker/config.json
// (which is bind-mounted from the host, so the host Docker daemon can read it)
// and returns a cleanup function that restores the previous config.
// If credentials are not configured it is a no-op.
func registryAuthEnv() (extraEnv []string, cleanup func(), err error) {
	cleanup = func() {}
	if env.RegistryURL == "" || env.RegistryUsername == "" || env.RegistryPassword == "" {
		return
	}

	auth := base64.StdEncoding.EncodeToString([]byte(env.RegistryUsername + ":" + env.RegistryPassword))
	configJSON := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, env.RegistryURL, auth)

	configDir := "/root/.docker"
	configPath := filepath.Join(configDir, "config.json")

	if err = os.MkdirAll(configDir, 0700); err != nil {
		return nil, func() {}, fmt.Errorf("failed to create docker config dir: %w", err)
	}

	// Back up any existing config so we can restore it.
	existing, readErr := os.ReadFile(configPath)

	if err = os.WriteFile(configPath, []byte(configJSON), 0600); err != nil {
		return nil, func() {}, fmt.Errorf("failed to write docker config: %w", err)
	}

	cleanup = func() {
		if readErr == nil {
			_ = os.WriteFile(configPath, existing, 0600)
		} else {
			_ = os.Remove(configPath)
		}
	}
	return
}
