package routers

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aidenappl/lattice-api/structs"
	"gopkg.in/yaml.v3"
)

func TestNormalizeUlimits(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr string
	}{
		{"single integer sets soft and hard", "nofile: 262144", `{"nofile":{"soft":262144,"hard":262144}}`, ""},
		{"soft/hard mapping", "nofile:\n  soft: 20000\n  hard: 40000", `{"nofile":{"soft":20000,"hard":40000}}`, ""},
		{"unlimited", "memlock: -1", `{"memlock":{"soft":-1,"hard":-1}}`, ""},
		{"several", "nproc: 65535\nnofile:\n  soft: 1\n  hard: 2", `{"nofile":{"soft":1,"hard":2},"nproc":{"soft":65535,"hard":65535}}`, ""},
		{"empty mapping", "{}", "", ""},
		{"soft above hard", "nofile:\n  soft: 5\n  hard: 4", "", "exceeds hard"},
		{"unknown name", "nofiles: 10", "", "unknown ulimit"},
		{"negative", "nofile: -5", "", "non-negative"},
		{"missing hard", "nofile:\n  soft: 5", "", "integer soft and hard"},
		{"string value", "nofile: lots", "", "integer or {soft, hard}"},
		{"not a mapping", "- nofile", "", "mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v any
			if err := yaml.Unmarshal([]byte(tt.yaml), &v); err != nil {
				t.Fatal(err)
			}
			got, err := normalizeUlimits(v)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
	if got, err := normalizeUlimits(nil); got != "" || err != nil {
		t.Errorf("nil ulimits = (%q, %v), want empty", got, err)
	}
}

func TestComposeUlimitsRejectsWholeFile(t *testing.T) {
	// Stack 30's ClickHouse service, plus a broken sibling.
	src := "services:\n  clickhouse:\n    image: clickhouse/clickhouse-server\n    ulimits:\n      nofile:\n        soft: 262144\n        hard: 262144\n  web:\n    image: web\n"
	var c composeFile
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	got, err := composeUlimits(c)
	if err != nil {
		t.Fatal(err)
	}
	if got["clickhouse"] != `{"nofile":{"soft":262144,"hard":262144}}` || got["web"] != "" {
		t.Errorf("composeUlimits = %v", got)
	}

	bad := src + "  db:\n    image: db\n    ulimits:\n      nofile:\n        soft: 9\n        hard: 1\n"
	c = composeFile{}
	if err := yaml.Unmarshal([]byte(bad), &c); err != nil {
		t.Fatal(err)
	}
	if _, err := composeUlimits(c); err == nil || !strings.Contains(err.Error(), "service db") {
		t.Errorf("err = %v, want it to name service db", err)
	}
}

func TestUlimitsFromBody(t *testing.T) {
	if got, err := ulimitsFromBody(nil); got != nil || err != nil {
		t.Errorf("nil body = (%v, %v), want untouched", got, err)
	}
	if got, err := ulimitsFromBody(strp("")); err != nil || got == nil || *got != "" {
		t.Errorf(`"" body = (%v, %v), want "" (clear)`, got, err)
	}
	got, err := ulimitsFromBody(strp(`{"nofile": 1024}`))
	if err != nil || *got != `{"nofile":{"soft":1024,"hard":1024}}` {
		t.Errorf("body = (%v, %v)", got, err)
	}
	if _, err := ulimitsFromBody(strp(`{"nofile": {"soft": 2, "hard": 1}}`)); err == nil {
		t.Error("soft > hard accepted")
	}
	if _, err := ulimitsFromBody(strp(`not json`)); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestUlimitsSpecSorted(t *testing.T) {
	got, err := ulimitsSpec(`{"nproc":{"soft":1,"hard":2},"nofile":{"soft":3,"hard":4}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"name": "nofile", "soft": int64(3), "hard": int64(4)},
		{"name": "nproc", "soft": int64(1), "hard": int64(2)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ulimitsSpec = %v, want %v", got, want)
	}
}

// Every deploy path builds through specBuilder; before it, CI deploys and
// rollbacks dropped networks and aliases, and rollbacks resolved healthcheck
// ${VAR}s without the container's own env. Both modes must send the same spec.
func TestSpecBuilderSameSpecForEveryPath(t *testing.T) {
	c := structs.Container{
		ID:             7,
		Name:           "monitor-clickhouse",
		Image:          "clickhouse/clickhouse-server",
		Tag:            "24.3-alpine",
		Replicas:       1,
		EnvVars:        strp(`{"PORT":"8123","DB":"${DB_NAME}"}`),
		Volumes:        strp(`{"clickhouse-data":"/var/lib/clickhouse"}`),
		HealthCheck:    strp(`{"test":["CMD-SHELL","curl localhost:${PORT}"]}`),
		Ulimits:        strp(`{"nofile":{"soft":262144,"hard":262144}}`),
		NetworkAliases: strp(`["ch"]`),
	}
	for _, rollback := range []bool{false, true} {
		b := &specBuilder{
			ctx:            context.Background(),
			env:            map[string]any{"DB_NAME": "monitor"},
			networks:       []string{"monitor-network"},
			composeAliases: map[string][]string{"monitor-clickhouse": {"clickhouse", "ch"}},
			rollback:       rollback,
		}
		spec, specErr := b.build(c, "clickhouse/clickhouse-server", "24.3-alpine")
		if specErr != nil {
			t.Fatalf("rollback=%v: %v", rollback, specErr.err)
		}
		raw, _ := json.Marshal(spec)
		got := string(raw)
		for _, want := range []string{
			`"networks":["monitor-network"]`,
			`"network_aliases":["ch","clickhouse"]`,
			`"DB":"monitor"`,
			`"curl localhost:8123"`,
			`"ulimits":[{"hard":262144,"name":"nofile","soft":262144}]`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("rollback=%v: spec missing %s\n%s", rollback, want, got)
			}
		}
	}
}

func TestSpecBuilderFatalAndSkippedFields(t *testing.T) {
	b := &specBuilder{ctx: context.Background(), env: map[string]any{}}
	_, specErr := b.build(structs.Container{Name: "x", EnvVars: strp("{broken")}, "img", "latest")
	if specErr == nil {
		t.Error("unreadable env vars must fail the deploy")
	}
	spec, specErr := b.build(structs.Container{Name: "x", Ulimits: strp("{broken"), Command: strp("nope")}, "img", "latest")
	if specErr != nil {
		t.Fatalf("an unreadable optional field must not fail the deploy: %v", specErr.err)
	}
	if _, ok := spec["ulimits"]; ok {
		t.Error("unparseable ulimits sent to the runner")
	}
	if _, ok := spec["command"]; ok {
		t.Error("unparseable command sent to the runner")
	}
}

// A field cleared through the edit form is stored as "", which must mean
// "unset" rather than malformed JSON logged as an error on every deploy.
func TestSpecBuilderClearedFieldsAreUnset(t *testing.T) {
	b := &specBuilder{ctx: context.Background(), env: map[string]any{}}
	empty := ""
	spec, specErr := b.build(structs.Container{
		Name: "x", PortMappings: &empty, Volumes: &empty, Command: &empty,
		Entrypoint: &empty, EnvVars: &empty, HealthCheck: &empty, Ulimits: &empty,
	}, "img", "latest")
	if specErr != nil {
		t.Fatalf("cleared fields failed the deploy: %v", specErr.err)
	}
	for _, k := range []string{"port_mappings", "volumes", "command", "entrypoint", "env_vars", "health_check", "ulimits"} {
		if _, ok := spec[k]; ok {
			t.Errorf("cleared %s still sent to the runner", k)
		}
	}
}

// A ${VAR} embedded in a longer env value (a DSN) must be interpolated, not
// only a value that is wholly a reference. Unknown refs stay literal.
func TestSpecBuilderInterpolatesEmbeddedEnvRefs(t *testing.T) {
	const dsnPrefix = "mysql://u:"
	b := &specBuilder{ctx: context.Background(), env: map[string]any{"DB_PASSWORD": "fixture-value", "PORT": float64(8080)}}
	spec, specErr := b.build(structs.Container{
		Name:    "x",
		EnvVars: strp(`{"DATABASE":"` + dsnPrefix + `${DB_PASSWORD}@db/x","PASS":"${DB_PASSWORD}","PORT":"${PORT}","ADDR":":$PORT","OTHER":"${MISSING}","PLAIN":"v"}`),
	}, "img", "latest")
	if specErr != nil {
		t.Fatalf("build failed: %v", specErr.err)
	}
	ev := spec["env_vars"].(map[string]any)
	want := map[string]any{
		"DATABASE": dsnPrefix + "fixture-value" + "@db/x",
		"PASS":     "fixture-value",
		"PORT":     float64(8080),
		"ADDR":     ":8080",
		"OTHER":    "${MISSING}",
		"PLAIN":    "v",
	}
	for k, w := range want {
		if ev[k] != w {
			t.Errorf("env_vars[%q] = %v, want %v", k, ev[k], w)
		}
	}
}
