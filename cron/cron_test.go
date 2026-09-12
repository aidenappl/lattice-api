package cron

import (
	"testing"
	"time"
)

// The matching behaviour itself is pinned by cron_test.go in package main,
// through the wrappers the schedulers call. These tests cover what this package
// adds: validation, and that validation and matching share one parser.

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every minute", "* * * * *", false},
		{"daily at 03:00", "0 3 * * *", false},
		{"steps, ranges and lists together", "*/15 9-17 * * 1-5", false},
		{"lists in several fields", "0 3,15 1 1,7 *", false},
		{"a stepped literal runs to the field max", "5/20 * * * *", false},
		{"four fields", "0 3 * *", true},
		{"six fields (seconds are not supported)", "0 0 3 * * *", true},
		{"garbage field", "0 abc * * *", true},
		{"zero step", "*/0 * * * *", true},
		{"inverted range", "0 17-9 * * *", true},
		{"empty list term", "0 3,,4 * * *", true},
		// The reason Validate exists at all: each of these parses, never matches,
		// and would otherwise save as a schedule that silently never fires.
		{"minute 60", "60 * * * *", true},
		{"day-of-week 7 is not Sunday here", "0 3 * * 7", true},
		{"month 13", "0 3 * 13 *", true},
		{"day-of-month 0", "0 3 0 * *", true},
		{"range running past the field max", "0 20-25 * * *", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.expr)
			if tt.wantErr && err == nil {
				t.Errorf("Validate(%q) accepted an expression that must be rejected", tt.expr)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tt.expr, err)
			}
		})
	}
}

// Anything Validate accepts must be something the evaluator can actually fire.
// If the two ever used different parsers this is where they would disagree.
func TestValidatedExpressionsActuallyFire(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, expr := range []string{
		"* * * * *",
		"0 3 * * *",
		"*/15 9-17 * * 1-5",
		"0 3,15 1 1,7 *",
		"5/20 * * * *",
		"30 4 1 * *",
	} {
		t.Run(expr, func(t *testing.T) {
			if err := Validate(expr); err != nil {
				t.Fatalf("precondition: %q should validate: %v", expr, err)
			}
			if fires := PreviousFires(expr, now, 1, 400*24*time.Hour); len(fires) == 0 {
				t.Errorf("%q validated but never fired in the lookback", expr)
			}
		})
	}
}

func TestParseTermMatchesTheOriginalSemantics(t *testing.T) {
	tests := []struct {
		term  string
		value int
		want  bool
	}{
		{"*", 37, true},
		{"5", 5, true},
		{"5", 6, false},
		{"*/5", 15, true},
		{"*/5", 17, false},
		{"10-20", 15, true},
		{"10-20", 21, false},
		{"10-20/5", 15, true},
		{"10-20/5", 16, false},
		{"5/20", 45, true},
		{"5/20", 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.term, func(t *testing.T) {
			got, err := termMatches(tt.term, tt.value, 0, 59)
			if err != nil {
				t.Fatalf("termMatches(%q): %v", tt.term, err)
			}
			if got != tt.want {
				t.Errorf("termMatches(%q, %d) = %v, want %v", tt.term, tt.value, got, tt.want)
			}
		})
	}
}
