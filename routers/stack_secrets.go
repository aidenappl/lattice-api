package routers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/aidenappl/lattice-api/middleware"
	"github.com/aidenappl/lattice-api/structs"
)

// secretMask is what a secret stack env value looks like to anyone who may not
// read it. It is also accepted back on update as "keep the stored value", so a
// client can save a stack's env vars without ever having seen its secrets.
const secretMask = "***"

// Stack env vars are stored as one JSON object (`env_vars`), and
// `secret_env_keys` is a JSON array naming the keys in it that are secret. The
// runner always receives real values: deploys read the stack row directly and
// never go through these helpers. Masking is for what the API hands to people.

// parseSecretKeys reads secret_env_keys. A malformed value is treated as "no
// secrets declared" rather than failing the request it is attached to.
func parseSecretKeys(raw *string) map[string]bool {
	keys := map[string]bool{}
	if raw == nil || *raw == "" {
		return keys
	}
	var list []string
	if err := json.Unmarshal([]byte(*raw), &list); err != nil {
		return keys
	}
	for _, k := range list {
		if k != "" {
			keys[k] = true
		}
	}
	return keys
}

// normalizeSecretKeys validates a client-supplied secret_env_keys value and
// returns it as a sorted, de-duplicated JSON array ("" for none).
func normalizeSecretKeys(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return "", fmt.Errorf("secret_env_keys must be a JSON array of key names")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, k := range list {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	sort.Strings(out)
	b, _ := json.Marshal(out)
	return string(b), nil
}

// maskEnvVars replaces the value of every secret key with secretMask, or with
// replacement when given. Env vars that aren't a JSON object are returned
// unchanged when there are no secrets, and withheld entirely when there are:
// an unparseable blob can't be partially masked.
func maskEnvVars(envVars *string, secret map[string]bool, replacement string) *string {
	if envVars == nil || *envVars == "" || len(secret) == 0 {
		return envVars
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(*envVars), &obj); err != nil {
		empty := ""
		return &empty
	}
	for k := range obj {
		if secret[k] {
			obj[k] = replacement
		}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		empty := ""
		return &empty
	}
	s := string(b)
	return &s
}

// mergeMaskedEnvVars resolves an incoming env_vars object against the stored
// one: any key that arrives as secretMask and exists in the stored object keeps
// its stored value. Everything else is taken as sent, so a key that was removed
// stays removed and a secret that was retyped is replaced.
func mergeMaskedEnvVars(stored *string, incoming string) (string, error) {
	if incoming == "" {
		return "", nil
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(incoming), &in); err != nil {
		return "", fmt.Errorf("env_vars must be a JSON object")
	}
	var old map[string]any
	if stored != nil && *stored != "" {
		_ = json.Unmarshal([]byte(*stored), &old)
	}
	for k, v := range in {
		if s, ok := v.(string); ok && s == secretMask {
			if prev, exists := old[k]; exists {
				in[k] = prev
			}
		}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// canReadSecrets mirrors global env vars: only admins see secret values.
func canReadSecrets(r *http.Request) bool {
	user, ok := middleware.GetUserFromContext(r.Context())
	return ok && user != nil && user.Role == "admin"
}

// stackForUser returns the stack as the requesting user may see it.
func stackForUser(r *http.Request, s *structs.Stack) *structs.Stack {
	if s == nil || canReadSecrets(r) {
		return s
	}
	masked := *s
	masked.EnvVars = maskEnvVars(s.EnvVars, parseSecretKeys(s.SecretEnvKeys), secretMask)
	return &masked
}

// stacksForUser is stackForUser over a list.
func stacksForUser(r *http.Request, stacks *[]structs.Stack) *[]structs.Stack {
	if stacks == nil || canReadSecrets(r) {
		return stacks
	}
	out := make([]structs.Stack, len(*stacks))
	for i := range *stacks {
		out[i] = *stackForUser(r, &(*stacks)[i])
	}
	return &out
}
