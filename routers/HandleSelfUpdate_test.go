package routers

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidenappl/lattice-api/env"
)

func TestShortImageID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"full sha256 digest", "sha256:9f2c5d1e8a3b4c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d", "9f2c5d1e8a3b"},
		{"bare hex", "9f2c5d1e8a3b4c6d7e8f", "9f2c5d1e8a3b"},
		{"short value passes through", "abc123", "abc123"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortImageID(tt.input); got != tt.want {
				t.Errorf("shortImageID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestPinnedTagHint covers the case that made a self-update look like it worked
// when it could not possibly have: lattice-api was pinned to an explicit version
// in the compose file, so pulling and recreating faithfully reproduced the same
// old image while the endpoint reported "pull complete, restarting".
func TestPinnedTagHint(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantHint bool
	}{
		{
			name:     "pinned version tag warns",
			ref:      "registry.appleby.cloud/lattice-api:v1.3.17",
			wantHint: true,
		},
		{
			name:     "latest does not warn",
			ref:      "registry.appleby.cloud/lattice-web:latest",
			wantHint: false,
		},
		{
			name:     "no tag means latest implicitly",
			ref:      "registry.appleby.cloud/lattice-api",
			wantHint: false,
		},
		{
			name:     "a registry port must not be mistaken for a tag",
			ref:      "registry.appleby.cloud:5000/lattice-api",
			wantHint: false,
		},
		{
			name:     "a registry port alongside a real tag still warns",
			ref:      "registry.appleby.cloud:5000/lattice-api:v1.3.17",
			wantHint: true,
		},
		{
			name:     "a registry port alongside latest does not warn",
			ref:      "registry.appleby.cloud:5000/lattice-api:latest",
			wantHint: false,
		},
		{
			name:     "a digest reference warns",
			ref:      "registry.appleby.cloud/lattice-api@sha256:9f2c5d1e8a3b4c6d",
			wantHint: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := pinnedTagHint(tt.ref)
			if tt.wantHint && hint == "" {
				t.Errorf("pinnedTagHint(%q) returned no hint; a pinned tag must be explained", tt.ref)
			}
			if !tt.wantHint && hint != "" {
				t.Errorf("pinnedTagHint(%q) = %q, want no hint", tt.ref, hint)
			}
			if tt.wantHint && !strings.Contains(hint, tt.ref) {
				t.Errorf("hint should name the offending ref, got %q", hint)
			}
		})
	}
}

func TestSafeServiceName(t *testing.T) {
	valid := []string{"lattice-api", "lattice_web", "api", "svc123"}
	for _, s := range valid {
		if !safeServiceName.MatchString(s) {
			t.Errorf("service name %q should be accepted", s)
		}
	}

	// These reach an exec.Command argument list, so anything that could be
	// interpreted as a separate token or flag must be rejected.
	invalid := []string{"", "-flag", "a b", "a;b", "a$(b)", "a&&b", "../etc", "a|b", "a\nb"}
	for _, s := range invalid {
		if safeServiceName.MatchString(s) {
			t.Errorf("service name %q should be rejected", s)
		}
	}
}

func TestUpsertEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		key      string
		value    string
		want     string
	}{
		{
			name:     "replaces an existing assignment in place",
			contents: "REGISTRY_URL=registry.example.com\nLATTICE_API_TAG=v1.3.20\nOTHER=keep\n",
			key:      "LATTICE_API_TAG",
			value:    "v1.3.21",
			want:     "REGISTRY_URL=registry.example.com\nLATTICE_API_TAG=v1.3.21\nOTHER=keep\n",
		},
		{
			name:     "appends when absent",
			contents: "REGISTRY_URL=registry.example.com\n",
			key:      "LATTICE_API_TAG",
			value:    "v1.3.21",
			want:     "REGISTRY_URL=registry.example.com\nLATTICE_API_TAG=v1.3.21\n",
		},
		{
			name:     "handles an empty file",
			contents: "",
			key:      "LATTICE_API_TAG",
			value:    "v1.3.21",
			want:     "LATTICE_API_TAG=v1.3.21\n",
		},
		{
			name:     "does not collapse a missing trailing newline into a joined line",
			contents: "REGISTRY_URL=registry.example.com",
			key:      "LATTICE_API_TAG",
			value:    "v1.3.21",
			want:     "REGISTRY_URL=registry.example.com\nLATTICE_API_TAG=v1.3.21\n",
		},
		{
			// A commented-out assignment must not be treated as the live one,
			// or the real value would be left untouched below it.
			name:     "ignores a commented assignment",
			contents: "# LATTICE_API_TAG=v1.0.0\nLATTICE_API_TAG=v1.3.20\n",
			key:      "LATTICE_API_TAG",
			value:    "v1.3.21",
			want:     "# LATTICE_API_TAG=v1.0.0\nLATTICE_API_TAG=v1.3.21\n",
		},
		{
			// Prefix collisions must not match.
			name:     "does not match a longer key sharing a prefix",
			contents: "LATTICE_API_TAG_OLD=v1.0.0\n",
			key:      "LATTICE_API_TAG",
			value:    "v1.3.21",
			want:     "LATTICE_API_TAG_OLD=v1.0.0\nLATTICE_API_TAG=v1.3.21\n",
		},
		{
			name:     "preserves comments and blank lines",
			contents: "# managed by hand\n\nFOO=1\n\n# trailing note\n",
			key:      "BAR",
			value:    "2",
			want:     "# managed by hand\n\nFOO=1\n\n# trailing note\nBAR=2\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(upsertEnvVar([]byte(tt.contents), tt.key, tt.value))
			if got != tt.want {
				t.Errorf("upsertEnvVar()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestSafeImageTag(t *testing.T) {
	valid := []string{"latest", "v1.3.21", "1.3.21", "v1.3.21-rc.1", "main-abc1234", "development"}
	for _, tag := range valid {
		if !safeImageTag.MatchString(tag) {
			t.Errorf("tag %q should be accepted", tag)
		}
	}

	// These are interpolated into an image reference and written to a file the
	// compose stack reads, so anything that could break out must be rejected.
	invalid := []string{
		"", "-leading", "v1.3.21 extra", "v1;rm -rf /", "v1$(id)", "v1\nFOO=bar",
		"v1`id`", "v1&&id", "../etc/passwd", "v1|id",
	}
	for _, tag := range invalid {
		if safeImageTag.MatchString(tag) {
			t.Errorf("tag %q should be rejected", tag)
		}
	}
}

func TestImageFromComposeConfig(t *testing.T) {
	// What `docker compose config lattice-api` prints: the named service AND its
	// depends_on, with mariadb first. The first-line reader returned mariadb:11.
	config := `name: lattice
services:
  lattice-api:
    depends_on:
      mariadb:
        condition: service_healthy
    image: registry.appleby.cloud/lattice-api:v1.3.34
  mariadb:
    image: mariadb:11
`
	tests := []struct {
		name    string
		out     string
		service string
		want    string
		wantErr bool
	}{
		{"named service, not its dependency", config, "lattice-api", "registry.appleby.cloud/lattice-api:v1.3.34", false},
		{"dependency by its own name", config, "mariadb", "mariadb:11", false},
		{"service missing", config, "lattice-web", "", true},
		{"service without an image", "services:\n  lattice-api:\n    build: .\n", "lattice-api", "", true},
		{"not yaml", "services: [", "lattice-api", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := imageFromComposeConfig([]byte(tt.out), tt.service)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("imageFromComposeConfig() = %q, %v; want %q, error=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestComposeDirWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	rw := t.TempDir()
	if err := ComposeDirWritable(rw); err != nil {
		t.Errorf("writable dir reported unwritable: %v", err)
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	if err := ComposeDirWritable(ro); err == nil {
		t.Error("read-only dir reported writable")
	}
}

func TestWriteComposeTagReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("LATTICE_API_TAG=v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	prev := env.DockerComposeDir
	env.DockerComposeDir = dir
	t.Cleanup(func() { env.DockerComposeDir = prev })

	err := writeComposeTag("LATTICE_API_TAG", "v1.2.3")
	if !errors.Is(err, errComposeNotWritable) {
		t.Fatalf("writeComposeTag on a read-only dir = %v, want errComposeNotWritable", err)
	}
	status, msg := versionErrorStatus(err)
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if !strings.Contains(msg, "read-write") {
		t.Errorf("message %q does not tell the operator to mount read-write", msg)
	}
}

func TestVersionErrorStatusRequestErrors(t *testing.T) {
	status, _ := versionErrorStatus(errors.New(`invalid version "x y": expected an image tag`))
	if status != http.StatusBadRequest {
		t.Errorf("request error status = %d, want 400", status)
	}
}

func TestServiceTagHintInterpolatedPin(t *testing.T) {
	dir := t.TempDir()
	compose := "services:\n  lattice-api:\n    image: registry.appleby.cloud/lattice-api:${LATTICE_API_TAG:-latest}\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := env.DockerComposeDir
	env.DockerComposeDir = dir
	t.Cleanup(func() { env.DockerComposeDir = prev })

	ref := "registry.appleby.cloud/lattice-api:v1.4.5"
	hint := serviceTagHint(ref, "LATTICE_API_TAG")
	if !strings.Contains(hint, "pass a version") || strings.Contains(hint, "edit the image tag") {
		t.Errorf("interpolated pin hint = %q, want 'pass a version' guidance", hint)
	}
	if got := serviceTagHint(ref, "SOME_OTHER_TAG"); !strings.Contains(got, "edit the image tag") {
		t.Errorf("hard-pinned hint = %q, want hand-edit guidance", got)
	}
	if got := serviceTagHint("registry.appleby.cloud/lattice-api:latest", "LATTICE_API_TAG"); got != "" {
		t.Errorf("latest hint = %q, want none", got)
	}
}
