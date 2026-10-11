package routers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
	"gopkg.in/yaml.v3"
)

// specBuilder builds the per-container spec every deploy path sends to a
// runner: dashboard deploys (HandleDeployStack), CI deploys (HandlePublicDeploy)
// and rollbacks (HandleRollbackDeployment). It is one builder so a field added
// for one path reaches all three. Before it existed each handler had its own
// copy, and CI deploys and rollbacks had silently stopped sending networks and
// network aliases, and rollbacks resolved healthcheck ${VAR}s without the
// container's own env.
type specBuilder struct {
	ctx            context.Context
	env            map[string]any // global env vars, stack's on top (loadDeployEnv)
	networks       []string
	composeAliases map[string][]string
	registries     *[]structs.Registry
	// rollback makes an unreadable optional field a warning instead of an error:
	// a rollback is recovering from a bad deploy and should not be blocked by a
	// field the bad deploy left malformed. Unreadable env vars or aliases still fail.
	rollback bool
}

// specError is a container field a deploy cannot proceed without. message is the
// static text for the client, err the cause recorded on the request's event.
type specError struct {
	message string
	err     error
}

func newSpecBuilder(ctx context.Context, stackID int, composeYAML *string, env map[string]any, rollback bool) *specBuilder {
	b := &specBuilder{ctx: ctx, env: env, rollback: rollback}
	// Every stack network goes on every container so Docker DNS resolves.
	if nets, _ := query.ListNetworksByStack(db.DB, stackID); nets != nil {
		for _, n := range *nets {
			b.networks = append(b.networks, n.Name)
		}
	}
	b.registries, _ = query.ListRegistries(db.DB)
	b.composeAliases = composeAliasMap(composeYAML)
	return b
}

// composeAliasMap maps container name -> compose service keys that differ from
// it. docker-compose adds the service key as a DNS alias automatically; this
// gives containers imported before the network_aliases column the same aliases.
func composeAliasMap(composeYAML *string) map[string][]string {
	aliases := make(map[string][]string)
	if composeYAML == nil || *composeYAML == "" {
		return aliases
	}
	var compose composeFile
	if yaml.Unmarshal([]byte(*composeYAML), &compose) != nil {
		return aliases
	}
	for svcKey, svc := range compose.Services {
		cName := svcKey
		if svc.ContainerName != "" {
			cName = svc.ContainerName
		}
		if cName != svcKey {
			aliases[cName] = append(aliases[cName], svcKey)
		}
	}
	return aliases
}

// skipField records an optional field that could not be parsed and is left off
// the spec.
func (b *specBuilder) skipField(c structs.Container, field string, err error) {
	if b.rollback {
		logger.WarnCtx(b.ctx, "deploy", "rollback: skipped an unparseable container field", logger.F{"container": c.Name, "field": field, "error": err})
		return
	}
	logger.ErrorCtx(b.ctx, "deploy", "invalid "+field+" JSON", logger.F{"container": c.Name, "error": err})
}

// build returns the runner spec for one container. image and tag are separate
// from c because a rollback deploys the previous deployment's image onto the
// live container record.
func (b *specBuilder) build(c structs.Container, image, tag string) (map[string]any, *specError) {
	spec := map[string]any{
		"id":             c.ID,
		"name":           c.Name,
		"image":          image,
		"tag":            tag,
		"replicas":       c.Replicas,
		"restart_policy": c.RestartPolicy,
	}

	if len(b.networks) > 0 {
		spec["networks"] = b.networks
	}

	aliases, err := parseNetworkAliases(c.NetworkAliases)
	if err != nil {
		return nil, &specError{"network aliases could not be read", fmt.Errorf("container %s: %w", c.Name, err)}
	}
	for _, a := range b.composeAliases[c.Name] {
		found := false
		for _, existing := range aliases {
			if existing == a {
				found = true
				break
			}
		}
		if !found {
			aliases = append(aliases, a)
		}
	}
	if len(aliases) > 0 {
		spec["network_aliases"] = aliases
	}

	if !jsonBlank(c.PortMappings) {
		var pm []any
		if err := json.Unmarshal([]byte(*c.PortMappings), &pm); err != nil {
			b.skipField(c, "port_mappings", err)
		} else {
			spec["port_mappings"] = resolveVarsInValue(pm, b.env)
		}
	}

	ev, err := parseContainerEnvVars(c.EnvVars)
	if err != nil {
		return nil, &specError{"container env vars could not be read", fmt.Errorf("container %s: %w", c.Name, err)}
	}
	if ev != nil {
		// Compose semantics: only the keys defined for the service, with ${VAR}
		// references resolved from the stack-level env.
		merged := make(map[string]any, len(ev))
		for k, v := range ev {
			if s, ok := v.(string); ok {
				if resolved, ok := resolveEnvRef(s, b.env); ok {
					merged[k] = resolved
					continue
				}
			}
			merged[k] = v
		}
		spec["env_vars"] = merged
	}

	if !jsonBlank(c.Volumes) {
		var vol map[string]any
		if err := json.Unmarshal([]byte(*c.Volumes), &vol); err != nil {
			b.skipField(c, "volumes", err)
		} else {
			spec["volumes"] = resolveVarsInValue(vol, b.env)
		}
	}

	if c.CPULimit != nil {
		spec["cpu_limit"] = *c.CPULimit
	}
	if c.MemoryLimit != nil {
		spec["memory_limit"] = int64(*c.MemoryLimit) * 1024 * 1024 // MB -> bytes for Docker
	}

	if !jsonBlank(c.Command) {
		var cmd []string
		if err := json.Unmarshal([]byte(*c.Command), &cmd); err != nil {
			b.skipField(c, "command", err)
		} else {
			spec["command"] = cmd
		}
	}
	if !jsonBlank(c.Entrypoint) {
		var ep []string
		if err := json.Unmarshal([]byte(*c.Entrypoint), &ep); err != nil {
			b.skipField(c, "entrypoint", err)
		} else {
			spec["entrypoint"] = ep
		}
	}

	if c.HasHealthCheck() {
		var hc map[string]any
		if err := json.Unmarshal([]byte(*c.HealthCheck), &hc); err != nil {
			b.skipField(c, "health_check", err)
		} else {
			// Healthcheck ${VAR}s resolve against the stack env with the
			// container's own env on top (container wins).
			all := make(map[string]any, len(b.env))
			for k, v := range b.env {
				all[k] = v
			}
			if containerEnvs, ok := spec["env_vars"].(map[string]any); ok {
				for k, v := range containerEnvs {
					all[k] = v
				}
			}
			spec["health_check"] = resolveVarsInValue(hc, all)
		}
	}

	if c.Ulimits != nil && *c.Ulimits != "" {
		if ul, err := ulimitsSpec(*c.Ulimits); err != nil {
			b.skipField(c, "ulimits", err)
		} else if len(ul) > 0 {
			spec["ulimits"] = ul
		}
	}

	if auth := b.registryAuth(c.RegistryID, image); auth != nil {
		spec["registry_auth"] = auth
	}
	return spec, nil
}

// registryAuth resolves credentials from the container's registry, or else by
// matching the image's hostname against every configured registry.
func (b *specBuilder) registryAuth(registryID *int, image string) map[string]string {
	creds := func(reg *structs.Registry) map[string]string {
		auth := map[string]string{}
		if reg.Username != nil {
			auth["username"] = *reg.Username
		}
		if reg.Password != nil {
			auth["password"] = *reg.Password
		}
		if len(auth) == 0 {
			return nil
		}
		return auth
	}
	if registryID != nil {
		if reg, err := query.GetRegistryByID(db.DB, *registryID); err == nil && reg != nil {
			return creds(reg)
		}
		return nil
	}
	if b.registries == nil {
		return nil
	}
	for _, reg := range *b.registries {
		regHost := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(reg.URL, "https://"), "http://"), "/")
		if strings.HasPrefix(image, regHost+"/") || image == regHost {
			auth := creds(&reg)
			if auth != nil {
				logger.InfoCtx(b.ctx, "deploy", "auto-matched registry", logger.F{"registry": reg.Name, "image": image})
			}
			return auth
		}
	}
	return nil
}

// ulimit is one resource limit in the stored (normalized) form.
type ulimit struct {
	Soft int64 `json:"soft"`
	Hard int64 `json:"hard"`
}

// ulimitsSpec turns the stored ulimits column ({"nofile":{"soft":n,"hard":n}})
// into the runner's list form, sorted by name so specs are deterministic.
func ulimitsSpec(raw string) ([]map[string]any, error) {
	var m map[string]ulimit
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "soft": m[n].Soft, "hard": m[n].Hard})
	}
	return out, nil
}

// knownUlimits are the names Docker accepts (go-units ulimitNameMapping).
var knownUlimits = map[string]bool{
	"core": true, "cpu": true, "data": true, "fsize": true, "locks": true, "memlock": true,
	"msgqueue": true, "nice": true, "nofile": true, "nproc": true, "rss": true,
	"rtprio": true, "rttime": true, "sigpending": true, "stack": true,
}

// normalizeUlimits validates a compose `ulimits` mapping and returns the stored
// JSON. Each entry is an integer (soft and hard both) or {soft, hard}, per the
// compose spec. -1 means unlimited. Returns "" for an empty mapping.
func normalizeUlimits(v any) (string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return "", nil
		}
		return "", fmt.Errorf("ulimits must be a mapping of name to limit")
	}
	toInt := func(x any) (int64, bool) {
		switch n := x.(type) {
		case int:
			return int64(n), true
		case int64:
			return n, true
		case float64:
			if n == float64(int64(n)) {
				return int64(n), true
			}
		}
		return 0, false
	}
	out := make(map[string]ulimit, len(m))
	for name, raw := range m {
		if !knownUlimits[name] {
			return "", fmt.Errorf("unknown ulimit %q", name)
		}
		var u ulimit
		if n, ok := toInt(raw); ok {
			u = ulimit{Soft: n, Hard: n}
		} else if pair, ok := raw.(map[string]any); ok {
			soft, okS := toInt(pair["soft"])
			hard, okH := toInt(pair["hard"])
			if !okS || !okH {
				return "", fmt.Errorf("ulimit %q needs integer soft and hard", name)
			}
			u = ulimit{Soft: soft, Hard: hard}
		} else {
			return "", fmt.Errorf("ulimit %q must be an integer or {soft, hard}", name)
		}
		if u.Soft < -1 || u.Hard < -1 {
			return "", fmt.Errorf("ulimit %q must be -1 (unlimited) or non-negative", name)
		}
		if u.Hard != -1 && (u.Soft == -1 || u.Soft > u.Hard) {
			return "", fmt.Errorf("ulimit %q soft limit exceeds hard limit", name)
		}
		out[name] = u
	}
	if len(out) == 0 {
		return "", nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// composeUlimits validates and normalizes every service's ulimits up front, so
// a bad value rejects the whole compose file before anything is written.
// Returns service key -> stored JSON ("" when the service sets none).
func composeUlimits(compose composeFile) (map[string]string, error) {
	out := make(map[string]string, len(compose.Services))
	for name, svc := range compose.Services {
		u, err := normalizeUlimits(svc.Ulimits)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		out[name] = u
	}
	return out, nil
}

// blankToNil treats an empty column as unset, so a container whose ulimits were
// cleared fingerprints the same as one that never had any.
func blankToNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// ulimitsFromBody validates a ulimits JSON string from an API body (create,
// update, stack import) and returns the normalized form. nil leaves the field
// untouched; "" clears it. The body uses the compose shape: each name maps to an
// integer or {soft, hard}.
func ulimitsFromBody(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	if *raw == "" {
		return raw, nil
	}
	var v any
	if err := json.Unmarshal([]byte(*raw), &v); err != nil {
		return nil, fmt.Errorf("ulimits must be a JSON object: %w", err)
	}
	norm, err := normalizeUlimits(v)
	if err != nil {
		return nil, err
	}
	return &norm, nil
}
