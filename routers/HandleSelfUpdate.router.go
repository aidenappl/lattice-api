package routers

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aidenappl/lattice-api/env"
	"github.com/aidenappl/lattice-api/responder"
)

// safeServiceName validates that a Docker service name contains only safe characters.
var safeServiceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func composeFilePath() string    { return filepath.Join(env.DockerComposeDir, "docker-compose.yml") }
func composeEnvFilePath() string { return filepath.Join(env.DockerComposeDir, ".env") }

// composeArgs builds a `docker compose` invocation against the deployment's
// compose file.
//
// Every invocation passes the same --env-file. The pull used to omit it while
// the recreate passed it, which meant a compose file interpolating something
// like ${REGISTRY_URL} could resolve a different image for each step.
func composeArgs(extra ...string) []string {
	args := []string{"compose", "-f", composeFilePath(), "--env-file", composeEnvFilePath()}
	return append(args, extra...)
}

// serviceImageRef returns the image reference compose resolves for a service,
// with ${VAR} interpolation already applied.
func serviceImageRef(service string, extraEnv []string) (string, error) {
	cmd := exec.Command("docker", composeArgs("config", "--images", service)...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve image for service %q: %w", service, err)
	}
	// One image per line; a single service yields one.
	for _, line := range strings.Split(string(out), "\n") {
		if ref := strings.TrimSpace(line); ref != "" {
			return ref, nil
		}
	}
	return "", fmt.Errorf("compose reported no image for service %q", service)
}

// runningImageID returns the image ID the service's running container was
// created from, or "" if the service isn't running or can't be inspected.
func runningImageID(service string, extraEnv []string) string {
	psCmd := exec.Command("docker", composeArgs("ps", "-q", service)...)
	psCmd.Env = append(os.Environ(), extraEnv...)
	psOut, err := psCmd.Output()
	if err != nil {
		return ""
	}
	containerID := strings.TrimSpace(string(psOut))
	if containerID == "" {
		return ""
	}

	inspectCmd := exec.Command("docker", "inspect", "-f", "{{.Image}}", containerID)
	inspectCmd.Env = append(os.Environ(), extraEnv...)
	out, err := inspectCmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// localImageID returns the ID of a locally-present image reference, or "".
func localImageID(ref string, extraEnv []string) string {
	cmd := exec.Command("docker", "image", "inspect", "-f", "{{.Id}}", ref)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// helperContainerRunning reports whether the docker-helper sidecar is up.
//
// The API cannot recreate itself directly — Docker kills every process in the
// container during the stop step — so the recreate is delegated to this sidecar.
// If it isn't running, the recreate silently never happens, which is
// indistinguishable from success unless it's checked up front.
func helperContainerRunning() bool {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", env.DockerHelperContainer)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// pinnedTagHint explains why a pull can be a no-op, when it can.
//
// A reference is pinned if it carries an explicit tag other than latest, or a
// digest. Note that the tag can only live in the final path segment: an earlier
// colon is a registry port, as in registry.example.com:5000/image.
func pinnedTagHint(ref string) string {
	segment := ref
	if idx := strings.LastIndex(ref, "/"); idx != -1 {
		segment = ref[idx+1:]
	}

	// A digest reference is pinned by definition and can never move.
	if strings.Contains(segment, "@") {
		return fmt.Sprintf(
			" This service is pinned to a digest (%q), so pulling can never move it to a newer build — "+
				"edit the image reference in %s and recreate.", ref, composeFilePath())
	}

	idx := strings.LastIndex(segment, ":")
	if idx == -1 {
		// No tag at all means :latest implicitly, which does move.
		return ""
	}
	if tag := segment[idx+1:]; tag == "" || tag == "latest" {
		return ""
	}

	return fmt.Sprintf(
		" The compose file pins this service to %q, so pulling can never move it to a newer build — "+
			"edit the image tag in %s and recreate.", ref, composeFilePath())
}

func HandleUpdateAPI(w http.ResponseWriter, r *http.Request) {
	if env.DockerComposeDir == "" {
		responder.SendError(w, http.StatusBadRequest, "self-update not configured: DOCKER_COMPOSE_DIR is not set")
		return
	}

	// Validate service name and compose dir to prevent command injection
	if !safeServiceName.MatchString(env.APIServiceName) {
		responder.SendError(w, http.StatusInternalServerError, "invalid API_SERVICE_NAME configuration")
		return
	}
	if !filepath.IsAbs(env.DockerComposeDir) {
		responder.SendError(w, http.StatusInternalServerError, "DOCKER_COMPOSE_DIR must be an absolute path")
		return
	}

	service := env.APIServiceName
	// force=true recreates even when the image hasn't changed, which is what you
	// want after editing environment variables rather than the image tag.
	force := r.URL.Query().Get("force") == "true"

	extraEnv, cleanup, err := registryAuthEnv()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to prepare registry credentials: %v", err))
		return
	}
	defer cleanup()

	imageRef, err := serviceImageRef(service, extraEnv)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// What the running container was built from, before we pull anything.
	previousImageID := runningImageID(service, extraEnv)

	// Pull latest image synchronously so we can report failures.
	pullCmd := exec.Command("docker", composeArgs("pull", service)...)
	pullCmd.Env = append(os.Environ(), extraEnv...)
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to pull API image: %v — %s", err, string(pullOut)))
		return
	}

	pulledImageID := localImageID(imageRef, extraEnv)

	// If the pull resolved to the image already running, recreating achieves
	// nothing. Saying so is the whole point: this endpoint used to report
	// "pull complete, restarting" either way, so an update that could never
	// change the version was indistinguishable from one that did — and the
	// obvious response was to run it again.
	if !force && previousImageID != "" && pulledImageID != "" && previousImageID == pulledImageID {
		detail := fmt.Sprintf("no-op: already running %s", imageRef)
		logAudit(r, "update", "api", nil, strPtr(detail))
		responder.New(w, map[string]any{
			"service":     service,
			"image":       imageRef,
			"image_id":    pulledImageID,
			"status":      "already up to date",
			"recreated":   false,
			"pinned":      pinnedTagHint(imageRef) != "",
			"hint":        strings.TrimSpace(pinnedTagHint(imageRef)),
			"force_param": "append ?force=true to recreate anyway (e.g. after an env change)",
		}, "API is already running the image this service resolves to — nothing to do."+pinnedTagHint(imageRef))
		return
	}

	// The recreate is delegated to the helper sidecar, so a missing helper means
	// the recreate silently never runs. Check before claiming success.
	if !helperContainerRunning() {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf(
			"cannot recreate the API container: helper container %q is not running. "+
				"The API cannot recreate itself directly, so the new image is pulled but cannot be applied. "+
				"Start the helper, or recreate manually: cd %s && docker compose up -d --force-recreate %s",
			env.DockerHelperContainer, env.DockerComposeDir, service))
		return
	}

	detail := fmt.Sprintf("recreating with %s", imageRef)
	if previousImageID != "" && pulledImageID != "" {
		detail = fmt.Sprintf("recreating with %s (%s -> %s)",
			imageRef, shortImageID(previousImageID), shortImageID(pulledImageID))
	}
	logAudit(r, "update", "api", nil, strPtr(detail))

	// Respond immediately before we trigger the recreate.
	responder.New(w, map[string]any{
		"service":        service,
		"image":          imageRef,
		"previous_image": shortImageID(previousImageID),
		"new_image":      shortImageID(pulledImageID),
		"status":         "pull complete, restarting",
		"recreated":      true,
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
		args := append([]string{"exec", env.DockerHelperContainer, "docker"},
			composeArgs("up", "-d", "--force-recreate", "--no-deps", "--pull", "never", service)...)
		execCmd := exec.Command("docker", args...)
		if out, err := execCmd.CombinedOutput(); err != nil {
			// An error here is expected if this container was killed before exec
			// returned — the compose command in the helper continues regardless.
			// Anything else is a genuine failure, but the response has already
			// gone out, so the audit entry above is the durable record that an
			// attempt was made.
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

	if !safeServiceName.MatchString(env.WebServiceName) {
		responder.SendError(w, http.StatusInternalServerError, "invalid WEB_SERVICE_NAME configuration")
		return
	}
	if !filepath.IsAbs(env.DockerComposeDir) {
		responder.SendError(w, http.StatusInternalServerError, "DOCKER_COMPOSE_DIR must be an absolute path")
		return
	}

	service := env.WebServiceName
	force := r.URL.Query().Get("force") == "true"

	extraEnv, cleanup, err := registryAuthEnv()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to prepare registry credentials: %v", err))
		return
	}
	defer cleanup()

	imageRef, err := serviceImageRef(service, extraEnv)
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	previousImageID := runningImageID(service, extraEnv)

	// Pull latest image
	pullCmd := exec.Command("docker", composeArgs("pull", service)...)
	pullCmd.Env = append(os.Environ(), extraEnv...)
	pullOut, err := pullCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to pull Web image: %v — %s", err, string(pullOut)))
		return
	}

	pulledImageID := localImageID(imageRef, extraEnv)

	if !force && previousImageID != "" && pulledImageID != "" && previousImageID == pulledImageID {
		detail := fmt.Sprintf("no-op: already running %s", imageRef)
		logAudit(r, "update", "web", nil, strPtr(detail))
		responder.New(w, map[string]any{
			"service":   service,
			"image":     imageRef,
			"image_id":  pulledImageID,
			"status":    "already up to date",
			"recreated": false,
			"pinned":    pinnedTagHint(imageRef) != "",
			"hint":      strings.TrimSpace(pinnedTagHint(imageRef)),
		}, "Web is already running the image this service resolves to — nothing to do."+pinnedTagHint(imageRef))
		return
	}

	// Recreate the web container — API stays running so we can respond, which
	// means unlike the API path this failure is reported directly.
	upCmd := exec.Command("docker", composeArgs("up", "-d", "--force-recreate", service)...)
	upCmd.Env = append(os.Environ(), extraEnv...)
	upOut, err := upCmd.CombinedOutput()
	if err != nil {
		responder.SendError(w, http.StatusInternalServerError, fmt.Sprintf("failed to recreate Web container: %v — %s", err, string(upOut)))
		return
	}

	logAudit(r, "update", "web", nil, strPtr(fmt.Sprintf("recreated with %s", imageRef)))
	responder.New(w, map[string]any{
		"service":        service,
		"image":          imageRef,
		"previous_image": shortImageID(previousImageID),
		"new_image":      shortImageID(pulledImageID),
		"recreated":      true,
	}, "Web update triggered successfully")
}

// shortImageID trims a sha256:... image ID to something readable, and passes
// through anything that doesn't look like one.
func shortImageID(id string) string {
	trimmed := strings.TrimPrefix(id, "sha256:")
	if len(trimmed) > 12 {
		return trimmed[:12]
	}
	return trimmed
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
