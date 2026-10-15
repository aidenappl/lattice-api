package routers

import (
	"encoding/json"
	"testing"
)

func decodeObj(t *testing.T, s *string) map[string]any {
	t.Helper()
	if s == nil {
		t.Fatal("nil env vars")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*s), &m); err != nil {
		t.Fatalf("not a JSON object: %q", *s)
	}
	return m
}

func TestParseSecretKeys(t *testing.T) {
	tests := []struct {
		name string
		raw  *string
		want int
	}{
		{"nil", nil, 0},
		{"empty", strp(""), 0},
		{"list", strp(`["A","B",""]`), 2},
		{"malformed is no secrets", strp(`{"A":1}`), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSecretKeys(tt.raw); len(got) != tt.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestNormalizeSecretKeys(t *testing.T) {
	got, err := normalizeSecretKeys(`["B","A","B",""]`)
	if err != nil || got != `["A","B"]` {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, _ := normalizeSecretKeys(`[]`); got != "" {
		t.Fatalf("empty list should clear, got %q", got)
	}
	if _, err := normalizeSecretKeys(`"A"`); err == nil {
		t.Fatal("expected an error for a non-array")
	}
}

func TestMaskEnvVars(t *testing.T) {
	env := strp(`{"DB_DSN":"admin:pw@tcp(db)","PORT":"8000"}`)
	secret := map[string]bool{"DB_DSN": true}

	m := decodeObj(t, maskEnvVars(env, secret, secretMask))
	if m["DB_DSN"] != secretMask || m["PORT"] != "8000" {
		t.Fatalf("got %v", m)
	}

	if got := maskEnvVars(env, nil, secretMask); got != env {
		t.Fatal("no secrets should return the value untouched")
	}
	if got := maskEnvVars(strp("not json"), secret, secretMask); *got != "" {
		t.Fatalf("unparseable env with secrets must be withheld, got %q", *got)
	}
	if blank := decodeObj(t, maskEnvVars(env, secret, "")); blank["DB_DSN"] != "" {
		t.Fatalf("custom replacement not applied: %v", blank)
	}
}

func TestMergeMaskedEnvVars(t *testing.T) {
	stored := strp(`{"DB_DSN":"real-secret","PORT":"8000","OLD":"x"}`)

	tests := []struct {
		name     string
		incoming string
		want     map[string]any
	}{
		{
			name:     "a masked value keeps the stored secret",
			incoming: `{"DB_DSN":"***","PORT":"9000"}`,
			want:     map[string]any{"DB_DSN": "real-secret", "PORT": "9000"},
		},
		{
			name:     "a retyped secret replaces it",
			incoming: `{"DB_DSN":"new-secret"}`,
			want:     map[string]any{"DB_DSN": "new-secret"},
		},
		{
			name:     "a masked value for a key that never existed is kept literally",
			incoming: `{"NEW":"***"}`,
			want:     map[string]any{"NEW": "***"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeMaskedEnvVars(stored, tt.incoming)
			if err != nil {
				t.Fatal(err)
			}
			m := decodeObj(t, &got)
			if len(m) != len(tt.want) {
				t.Fatalf("got %v, want %v", m, tt.want)
			}
			for k, v := range tt.want {
				if m[k] != v {
					t.Errorf("%s: got %v, want %v", k, m[k], v)
				}
			}
		})
	}

	if got, err := mergeMaskedEnvVars(stored, ""); err != nil || got != "" {
		t.Fatalf("clearing: got %q, %v", got, err)
	}
	if _, err := mergeMaskedEnvVars(stored, "[1,2]"); err == nil {
		t.Fatal("expected an error for a non-object")
	}
}
