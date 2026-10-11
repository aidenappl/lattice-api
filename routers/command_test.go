package routers

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr string
	}{
		{"npm start", []string{"npm", "start"}, ""},
		{`sh -c "echo hi && run"`, []string{"sh", "-c", "echo hi && run"}, ""},
		{`["npm", "start"]`, []string{"npm", "start"}, ""},
		{`["sh", "-c", "a && b"]`, []string{"sh", "-c", "a && b"}, ""},
		{`echo 'single quoted' plain\ escaped`, []string{"echo", "single quoted", "plain escaped"}, ""},
		{"echo $HOME", []string{"echo", "$HOME"}, ""}, // no expansion: the container's shell does that, if any
		{"   ", nil, ""},
		{"echo a && echo b", nil, "unquoted shell operator"},
		{"a | b", nil, "unquoted shell operator"},
		{"a ; b", nil, "unquoted shell operator"},
		{`a "unbalanced`, nil, "cannot split"},
		{`["npm", 1]`, nil, "list of strings"},
		{`[not json`, nil, "list of strings"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseCommand(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCommand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCommandFromBody(t *testing.T) {
	if got, err := commandFromBody("command", nil); got != nil || err != nil {
		t.Errorf("nil = (%v, %v), want untouched", got, err)
	}
	if got, _ := commandFromBody("command", strp("")); got == nil || *got != "" {
		t.Errorf(`"" must clear, got %v`, got)
	}
	got, err := commandFromBody("command", strp(`sh -c "echo ok && sleep 3600"`))
	if err != nil || *got != `["sh","-c","echo ok \u0026\u0026 sleep 3600"]` {
		t.Errorf("stored = (%v, %v)", got, err)
	}
	if _, err := commandFromBody("entrypoint", strp("a && b")); err == nil || !strings.HasPrefix(err.Error(), "entrypoint:") {
		t.Errorf("err = %v, want it to name the field", err)
	}
}

// Compose string commands are shell-split: quoted arguments stay whole. They
// used to be split on whitespace, breaking `sh -c "..."`.
func TestComposeCommandsShellSplit(t *testing.T) {
	src := "services:\n  app:\n    image: x\n    command: sh -c \"migrate && serve\"\n    entrypoint: [\"/init\", \"--\"]\n"
	var c composeFile
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if err := validateComposeCommands(c); err != nil {
		t.Fatal(err)
	}
	if got := parseStringOrList(c.Services["app"].Command); !reflect.DeepEqual(got, []string{"sh", "-c", "migrate && serve"}) {
		t.Errorf("command = %q", got)
	}
	if got := parseStringOrList(c.Services["app"].Entrypoint); !reflect.DeepEqual(got, []string{"/init", "--"}) {
		t.Errorf("entrypoint = %q", got)
	}

	bad := "services:\n  app:\n    image: x\n    command: migrate && serve\n"
	c = composeFile{}
	if err := yaml.Unmarshal([]byte(bad), &c); err != nil {
		t.Fatal(err)
	}
	if err := validateComposeCommands(c); err == nil || !strings.Contains(err.Error(), "service app: command") {
		t.Errorf("err = %v, want it to name service and field", err)
	}
}
