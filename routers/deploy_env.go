package routers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aidenappl/lattice-api/crypto"
	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
)

// Swappable in tests.
var (
	listGlobalEnvVars = query.ListGlobalEnvVars
	decryptEnvValue   = crypto.Decrypt
)

// jsonBlank reports whether a stored JSON column holds nothing: NULL, an empty
// string or a JSON null. Existing rows store all three for "no value", so none
// of them may fail a deploy.
func jsonBlank(raw *string) bool {
	if raw == nil {
		return true
	}
	s := strings.TrimSpace(*raw)
	return s == "" || s == "null"
}

// parseStackEnvVars reads a stack's env_vars column. A blank column is an
// empty map; a column that does not parse is an error, since deploying
// without the stack's env vars would ship a silently misconfigured stack.
func parseStackEnvVars(raw *string) (map[string]any, error) {
	out := map[string]any{}
	if jsonBlank(raw) {
		return out, nil
	}
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// parseContainerEnvVars reads a container's env_vars column. It returns a nil
// map when the column is blank, so the caller can leave env_vars off the spec.
func parseContainerEnvVars(raw *string) (map[string]any, error) {
	if jsonBlank(raw) {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseNetworkAliases reads a container's network_aliases column. A blank
// column is no aliases.
func parseNetworkAliases(raw *string) ([]string, error) {
	if jsonBlank(raw) {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// deployEnvError is a deploy env failure: message is the static text for the
// client, err the cause recorded on the request's event.
type deployEnvError struct {
	message string
	err     error
}

func (e *deployEnvError) Error() string { return e.message + ": " + e.err.Error() }
func (e *deployEnvError) Unwrap() error { return e.err }

// loadDeployEnv builds the env a deploy resolves ${VAR} references against:
// global env vars as the base layer, the stack's own on top (stack wins).
// Any part that cannot be read fails the deploy rather than shipping without it.
func loadDeployEnv(stackEnvVars *string) (map[string]any, *deployEnvError) {
	merged := make(map[string]any)

	globalVars, err := listGlobalEnvVars(db.DB)
	if err != nil {
		return nil, &deployEnvError{"global env vars could not be read", err}
	}
	if globalVars != nil {
		for _, gv := range *globalVars {
			decrypted, err := decryptEnvValue(gv.EncryptedValue)
			if err != nil {
				return nil, &deployEnvError{"global env var could not be decrypted", fmt.Errorf("key %s: %w", gv.Key, err)}
			}
			merged[gv.Key] = decrypted
		}
	}

	stackVars, err := parseStackEnvVars(stackEnvVars)
	if err != nil {
		return nil, &deployEnvError{"stack env vars could not be read", err}
	}
	for k, v := range stackVars {
		merged[k] = v
	}
	return merged, nil
}
