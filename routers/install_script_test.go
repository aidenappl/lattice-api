package routers

import (
	"os"
	"strings"
	"testing"
)

// install/runner.sh is embedded into the API, served at /install/runner, and
// run on every worker by upgrade_runner. It writes the runner's systemd unit in
// two places — the upgrade path and the fresh install — and neither may depend
// on Docker with Requires=: one failed Docker start job leaves such a unit
// "Dependency failed" and never retried, because Restart=always only covers the
// process exiting. The upgrade path must also migrate an existing Requires=
// unit, or a fleet upgrade never repairs the workers that most need it.
//
// These units must match lattice-runner's cmd/setup.go serviceTemplate.
func TestInstallScriptRunnerUnit(t *testing.T) {
	raw, err := os.ReadFile("../install/runner.sh")
	if err != nil {
		t.Fatalf("read install/runner.sh: %v", err)
	}
	script := string(raw)

	units := unitHeredocs(script)
	if len(units) != 2 {
		t.Fatalf("expected 2 systemd unit heredocs (upgrade + fresh install), found %d", len(units))
	}

	lines := []struct {
		line    string
		present bool
	}{
		{"After=network.target docker.service", true},
		{"Wants=docker.service", true},
		{"PartOf=docker.service", true},
		{"Restart=always", true},
		{"Requires=docker.service", false},
	}

	for i, unit := range units {
		for _, tt := range lines {
			name := "unit" + string(rune('1'+i)) + "/" + tt.line
			t.Run(name, func(t *testing.T) {
				if got := hasLine(unit, tt.line); got != tt.present {
					t.Errorf("unit %d contains %q = %v, want %v", i+1, tt.line, got, tt.present)
				}
			})
		}
	}

	t.Run("upgrade path migrates existing units", func(t *testing.T) {
		const migrate = `sed -i 's/^Requires=docker\.service$/Wants=docker.service\nPartOf=docker.service/'`
		if !strings.Contains(script, migrate) {
			t.Error("install/runner.sh no longer migrates an existing Requires=docker.service unit on upgrade")
		}
	})
}

// unitHeredocs returns the body of every <<SVCEOF heredoc in the script.
func unitHeredocs(script string) []string {
	const open, close = "<<SVCEOF\n", "\nSVCEOF\n"
	var units []string
	for {
		start := strings.Index(script, open)
		if start < 0 {
			return units
		}
		script = script[start+len(open):]
		end := strings.Index(script, close)
		if end < 0 {
			return units
		}
		units = append(units, script[:end+1])
		script = script[end+len(close):]
	}
}

func hasLine(text, line string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}
