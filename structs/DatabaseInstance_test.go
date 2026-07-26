package structs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDatabaseStatusIsValid(t *testing.T) {
	valid := []DatabaseStatus{
		DBStatusPending, DBStatusProvisioning, DBStatusRunning, DBStatusStopped,
		DBStatusRestarting, DBStatusDegraded, DBStatusDeleting, DBStatusError,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("status %q should be valid", s)
		}
	}

	// The status column accepted any string before this existed, so a typo or a
	// stray action-outcome value like "success" could be written into it — which
	// then rendered as an unstyled grey badge no branch in the UI matched.
	invalid := []DatabaseStatus{"", "success", "failed", "creating", "Running", "unknown"}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("status %q should be rejected", s)
		}
	}
}

func TestDatabaseStatusIsTransitional(t *testing.T) {
	tests := map[DatabaseStatus]bool{
		DBStatusPending:      true,
		DBStatusProvisioning: true,
		DBStatusRestarting:   true,
		DBStatusDeleting:     true,
		// Steady states, however unhappy — the watchdog must not touch these.
		DBStatusRunning:  false,
		DBStatusStopped:  false,
		DBStatusError:    false,
		DBStatusDegraded: false,
	}

	for status, want := range tests {
		t.Run(string(status), func(t *testing.T) {
			if got := status.IsTransitional(); got != want {
				t.Errorf("%q.IsTransitional() = %v, want %v", status, got, want)
			}
		})
	}
}

func TestDatabaseHealthIsValid(t *testing.T) {
	for _, h := range []DatabaseHealth{DBHealthNone, DBHealthStarting, DBHealthHealthy, DBHealthUnhealthy} {
		if !h.IsValid() {
			t.Errorf("health %q should be valid", h)
		}
	}
	for _, h := range []DatabaseHealth{"", "ok", "Healthy", "degraded"} {
		if h.IsValid() {
			t.Errorf("health %q should be rejected", h)
		}
	}
}

// TestDatabaseErrorRoundTrip guards the JSON shape, since last_error is stored
// as a TEXT column and parsed back out on every read.
func TestDatabaseErrorRoundTrip(t *testing.T) {
	original := DatabaseError{
		Code:       DBErrCodeProvisionTimeout,
		Message:    "stuck in provisioning for more than 10m0s",
		OccurredAt: time.Now().UTC().Truncate(time.Second),
		Retryable:  true,
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded DatabaseError
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Code != original.Code {
		t.Errorf("code = %q, want %q", decoded.Code, original.Code)
	}
	if decoded.Message != original.Message {
		t.Errorf("message = %q, want %q", decoded.Message, original.Message)
	}
	if !decoded.OccurredAt.Equal(original.OccurredAt) {
		t.Errorf("occurred_at = %v, want %v", decoded.OccurredAt, original.OccurredAt)
	}
	if decoded.Retryable != original.Retryable {
		t.Errorf("retryable = %v, want %v", decoded.Retryable, original.Retryable)
	}
}

// TestDatabaseInstanceNeverSerialisesSecrets is the guard that matters most
// here: the struct is returned directly from list and get endpoints, so a
// missing json:"-" tag would publish every database password to any caller who
// can read an instance.
func TestDatabaseInstanceNeverSerialisesSecrets(t *testing.T) {
	root := "root-secret-value"
	password := "app-secret-value"

	instance := DatabaseInstance{
		ID:           1,
		Name:         "testdb",
		RootPassword: &root,
		Password:     &password,
	}

	encoded, err := json.Marshal(instance)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	body := string(encoded)
	for _, secret := range []string{root, password, "root_password", `"password"`} {
		if strings.Contains(body, secret) {
			t.Errorf("serialised instance leaks %q: %s", secret, body)
		}
	}
}
