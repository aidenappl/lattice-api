package routers

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/socket"
	"github.com/aidenappl/lattice-api/structs"
)

func strp(s string) *string { return &s }

func TestParseStackEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		raw     *string
		want    map[string]any
		wantErr bool
	}{
		{"nil", nil, map[string]any{}, false},
		{"empty", strp(""), map[string]any{}, false},
		{"whitespace", strp("   "), map[string]any{}, false},
		{"json null", strp("null"), map[string]any{}, false},
		{"padded json null", strp(" null\n"), map[string]any{}, false},
		{"valid", strp(`{"A":"1","B":2}`), map[string]any{"A": "1", "B": float64(2)}, false},
		{"corrupt", strp(`{"A":`), nil, true},
		{"wrong shape", strp(`["A"]`), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStackEnvVars(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got == nil || len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("got[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestParseContainerEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		raw     *string
		wantNil bool
		wantErr bool
	}{
		{"nil", nil, true, false},
		{"empty", strp(""), true, false},
		{"json null", strp("null"), true, false},
		{"empty object", strp("{}"), false, false},
		{"valid", strp(`{"A":"${X}"}`), false, false},
		{"corrupt", strp(`{A}`), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseContainerEnvVars(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if (got == nil) != tt.wantNil {
				t.Errorf("got %v, wantNil %v", got, tt.wantNil)
			}
		})
	}
}

func TestParseNetworkAliases(t *testing.T) {
	tests := []struct {
		name    string
		raw     *string
		want    int
		wantErr bool
	}{
		{"nil", nil, 0, false},
		{"empty", strp(" "), 0, false},
		{"json null", strp("null"), 0, false},
		{"valid", strp(`["db","cache"]`), 2, false},
		{"corrupt", strp(`["db"`), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNetworkAliases(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(got) != tt.want {
				t.Errorf("got %v, want %d aliases", got, tt.want)
			}
		})
	}
}

func TestLoadDeployEnv(t *testing.T) {
	origList, origDecrypt := listGlobalEnvVars, decryptEnvValue
	t.Cleanup(func() { listGlobalEnvVars, decryptEnvValue = origList, origDecrypt })

	globals := &[]structs.GlobalEnvVar{{Key: "A", EncryptedValue: "enc-a"}, {Key: "B", EncryptedValue: "enc-b"}}
	okList := func(db.Queryable) (*[]structs.GlobalEnvVar, error) { return globals, nil }
	okDecrypt := func(s string) (string, error) { return strings.TrimPrefix(s, "enc-"), nil }

	tests := []struct {
		name     string
		list     func(db.Queryable) (*[]structs.GlobalEnvVar, error)
		decrypt  func(string) (string, error)
		stack    *string
		wantMsg  string
		wantInEr string
		want     map[string]any
	}{
		{name: "stack wins over global", list: okList, decrypt: okDecrypt, stack: strp(`{"B":"stack"}`), want: map[string]any{"A": "a", "B": "stack"}},
		{name: "blank stack env", list: okList, decrypt: okDecrypt, stack: strp(""), want: map[string]any{"A": "a", "B": "b"}},
		{name: "global list fails", list: func(db.Queryable) (*[]structs.GlobalEnvVar, error) { return nil, errors.New("bad connection") }, decrypt: okDecrypt, wantMsg: "global env vars could not be read"},
		{name: "decrypt fails", list: okList, decrypt: func(s string) (string, error) {
			if s == "enc-b" {
				return "", errors.New("cipher: message authentication failed")
			}
			return okDecrypt(s)
		}, wantMsg: "global env var could not be decrypted", wantInEr: "key B"},
		{name: "corrupt stack env", list: okList, decrypt: okDecrypt, stack: strp(`{"B":`), wantMsg: "stack env vars could not be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listGlobalEnvVars, decryptEnvValue = tt.list, tt.decrypt
			got, envErr := loadDeployEnv(tt.stack)
			if tt.wantMsg != "" {
				if envErr == nil {
					t.Fatalf("got %v, want failure %q", got, tt.wantMsg)
				}
				if envErr.message != tt.wantMsg {
					t.Errorf("message = %q, want %q", envErr.message, tt.wantMsg)
				}
				if !strings.Contains(envErr.err.Error(), tt.wantInEr) {
					t.Errorf("err = %v, want it to contain %q", envErr.err, tt.wantInEr)
				}
				return
			}
			if envErr != nil {
				t.Fatalf("unexpected failure: %v", envErr)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("got[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestDispatchErrorMessageIsStatic(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"not connected", fmt.Errorf("%w: %d", socket.ErrWorkerNotConnected, 7), "failed to send deploy command: worker is not connected"},
		{"queue full", fmt.Errorf("%w: %d", socket.ErrSendQueueFull, 7), "failed to send deploy command: worker send queue is full"},
		{"other", errors.New("marshal payload: json: unsupported type"), "failed to send deploy command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dispatchErrorMessage("deploy", tt.err); got != tt.want {
				t.Errorf("message = %q, want %q", got, tt.want)
			}
		})
	}
}
