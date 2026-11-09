package routers

import (
	"errors"
	"strings"
	"testing"

	"github.com/aidenappl/lattice-api/env"
)

func withRegistry(t *testing.T, url, user, pass string) {
	t.Helper()
	pu, puser, ppass := env.RegistryURL, env.RegistryUsername, env.RegistryPassword
	env.RegistryURL, env.RegistryUsername, env.RegistryPassword = url, user, pass
	t.Cleanup(func() { env.RegistryURL, env.RegistryUsername, env.RegistryPassword = pu, puser, ppass })
}

func TestRegistryRepo(t *testing.T) {
	withRegistry(t, "https://registry.appleby.cloud", "u", "p")
	tests := []struct {
		ref, repo string
		ok        bool
	}{
		{"registry.appleby.cloud/lattice-web:latest", "lattice-web", true},
		{"registry.appleby.cloud/lattice-web", "lattice-web", true},
		{"registry.appleby.cloud/team/app:v1@sha256:abc", "team/app", true},
		{"docker.io/library/mariadb:11", "", false},
		{"registry.appleby.cloud.evil/x:1", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			repo, ok := registryRepo(tt.ref)
			if repo != tt.repo || ok != tt.ok {
				t.Fatalf("registryRepo(%q) = %q, %v; want %q, %v", tt.ref, repo, ok, tt.repo, tt.ok)
			}
		})
	}
}

// The 2026-11-08 incident: a web update pinned v1.8.8, which lattice-web never
// publishes, and only the pull failed. It must be refused before the pin.
func TestCheckTargetTag(t *testing.T) {
	withRegistry(t, "registry.appleby.cloud", "u", "p")
	prev := tagExists
	t.Cleanup(func() { tagExists = prev })
	published := map[string]bool{"latest": true, "main-aa74cd6": true}
	tagExists = func(repo, tag string) (bool, error) {
		if tag == "unreachable" {
			return false, errors.New("registry unreachable")
		}
		return repo == "lattice-web" && published[tag], nil
	}
	ref := "registry.appleby.cloud/lattice-web:latest"

	tests := []struct {
		name, ref, tag string
		wantNotFound   bool
	}{
		{"published tag", ref, "main-aa74cd6", false},
		{"missing release tag", ref, "v1.8.8", true},
		{"registry unreachable lets the pull decide", ref, "unreachable", false},
		{"other registry is not checked", "docker.io/library/nginx:1", "v1.8.8", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkTargetTag(tt.ref, "lattice-web", tt.tag)
			if got := errors.Is(err, errTagNotFound); got != tt.wantNotFound {
				t.Fatalf("checkTargetTag(%q) = %v, wantNotFound %v", tt.tag, err, tt.wantNotFound)
			}
			if tt.wantNotFound {
				if status, _ := versionErrorStatus(err); status != 400 {
					t.Errorf("status = %d, want 400", status)
				}
				if !strings.Contains(err.Error(), "lattice-web:v1.8.8") {
					t.Errorf("message %q does not name the missing image", err)
				}
			}
		})
	}

	withRegistry(t, "registry.appleby.cloud", "", "")
	if err := checkTargetTag(ref, "lattice-web", "v1.8.8"); err != nil {
		t.Errorf("without registry credentials the check must be skipped, got %v", err)
	}
}
