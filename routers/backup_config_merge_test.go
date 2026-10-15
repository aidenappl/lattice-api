package routers

import (
	"encoding/json"
	"testing"
)

func TestMergeBackupConfig(t *testing.T) {
	stored := `{"bucket":"b","region":"us-east-1","access_key_id":"AK","secret_access_key":"SK","path_prefix":"old/"}`

	tests := []struct {
		name     string
		stored   *string
		patch    map[string]any
		sameType bool
		want     map[string]any
	}{
		{
			name:     "a partial edit keeps every field it did not mention",
			stored:   &stored,
			patch:    map[string]any{"path_prefix": "new/"},
			sameType: true,
			want: map[string]any{
				"bucket": "b", "region": "us-east-1", "access_key_id": "AK",
				"secret_access_key": "SK", "path_prefix": "new/",
			},
		},
		{
			name:     "null removes an optional field",
			stored:   &stored,
			patch:    map[string]any{"path_prefix": nil},
			sameType: true,
			want: map[string]any{
				"bucket": "b", "region": "us-east-1", "access_key_id": "AK", "secret_access_key": "SK",
			},
		},
		{
			name:     "a type change replaces the config entirely",
			stored:   &stored,
			patch:    map[string]any{"server": "nas", "share": "backups"},
			sameType: false,
			want:     map[string]any{"server": "nas", "share": "backups"},
		},
		{
			name:     "no stored config",
			stored:   nil,
			patch:    map[string]any{"bucket": "x"},
			sameType: true,
			want:     map[string]any{"bucket": "x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeBackupConfig(tt.stored, tt.patch, tt.sameType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var gotMap map[string]any
			if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
				t.Fatalf("result is not JSON: %v", err)
			}
			if len(gotMap) != len(tt.want) {
				t.Fatalf("got %v, want %v", gotMap, tt.want)
			}
			for k, v := range tt.want {
				if gotMap[k] != v {
					t.Errorf("key %q: got %v, want %v", k, gotMap[k], v)
				}
			}
		})
	}
}

func TestMergeBackupConfigRejectsCorruptStoredConfig(t *testing.T) {
	bad := "not json"
	if _, err := mergeBackupConfig(&bad, map[string]any{"a": "b"}, true); err == nil {
		t.Fatal("expected an error for a stored config that is not JSON")
	}
}
