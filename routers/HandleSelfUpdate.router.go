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
	// We cannot run the recreate command inside this container: Docker kills all
	// processes in the container (including this goroutine) as part of the stop
	// step, so create/start never execute.
	//
	// We also cannot use `docker run --rm -d docker:cli ...` because that image
	// may not be cached at update time.
	//
	// Solution: exec into the persistent docker-helper sidecar, which runs
	// docker:cli with the socket and compose dir mounted and is immune to this
	// container's lifecycle. `docker exec` without -d means the exec session
	// is attached, but the process inside the helper container continues even if
	// this container dies before the command finishes.
	go func() {
		time.Sleep(2 * time.Second)
		execCmd := exec.Command(
			"docker", "exec",
			env.DockerHelperContainer,
			"docker", "compose",
			"-f", composeFile,
			"--env-file", env.DockerComposeDir+"/.env",
			"up", "-d",
			"--force-recreate",
			"--no-deps",
			"--pull", "never",
			service,
		)
		if out, err := execCmd.CombinedOutput(); err != nil {
			// An error here is expected if this container was killed before exec
			// returned — the compose command in the helper continues regardless.
			log.Printf("API self-update exec returned (may be benign disconnect): %v — %s", err, string(out))
		} else {
			log.Printf("API self-update exec completed: %s", string(out))
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
