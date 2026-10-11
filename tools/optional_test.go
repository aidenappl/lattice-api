package tools

import (
	"encoding/json"
	"testing"
)

func TestOptional(t *testing.T) {
	type body struct {
		CPU Optional[float64] `json:"cpu_limit"`
	}
	tests := []struct {
		name      string
		in        string
		set       bool
		clear     bool
		wantValue *float64
	}{
		{"absent leaves unchanged", `{}`, false, false, nil},
		{"null clears", `{"cpu_limit": null}`, true, true, nil},
		{"value sets", `{"cpu_limit": 0.5}`, true, false, ptr(0.5)},
		{"zero is a value, not a clear", `{"cpu_limit": 0}`, true, false, ptr(0.0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b body
			if err := json.Unmarshal([]byte(tt.in), &b); err != nil {
				t.Fatal(err)
			}
			if b.CPU.Set != tt.set || b.CPU.Clear() != tt.clear {
				t.Errorf("Set=%v Clear=%v, want %v %v", b.CPU.Set, b.CPU.Clear(), tt.set, tt.clear)
			}
			if (b.CPU.Value == nil) != (tt.wantValue == nil) || (b.CPU.Value != nil && *b.CPU.Value != *tt.wantValue) {
				t.Errorf("Value = %v, want %v", b.CPU.Value, tt.wantValue)
			}
		})
	}
	var b body
	if err := json.Unmarshal([]byte(`{"cpu_limit": "lots"}`), &b); err == nil {
		t.Error("a wrong-typed value must be a decode error")
	}
}

func ptr[T any](v T) *T { return &v }
