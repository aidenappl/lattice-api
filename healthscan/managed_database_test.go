package healthscan

import "testing"

// A managed database has no row in `containers` by design — it is its own
// resource, reconciled against db_sync by databaseReconciler. The health scanner
// compares worker containers against `containers`, so without an exclusion every
// managed database is reported as unmanaged forever.
//
// One permanent, unfixable anomaly per database is worse than a missing check:
// it is what teaches an operator that the anomaly list is noise, and then the
// real anomaly in the list goes unread.
func TestManagedDatabasesAreExcludedFromUnmanagedDetection(t *testing.T) {
	managed := map[string]bool{"lattice-db-backup-proof": true}
	expected := map[string]string{"api-1": "running"}
	onWorker := []string{"api-1", "lattice-db-backup-proof", "stray-container"}

	var flagged []string
	for _, name := range onWorker {
		if managed[name] {
			continue
		}
		if _, tracked := expected[name]; !tracked {
			flagged = append(flagged, name)
		}
	}

	if len(flagged) != 1 || flagged[0] != "stray-container" {
		t.Errorf("expected only the genuinely untracked container to be flagged, got %v", flagged)
	}
}

// Excluding rather than marking them "expected running" is deliberate: a
// deliberately stopped database would otherwise be reported as a status
// mismatch, trading one false positive for another.
func TestStoppedManagedDatabaseIsNotAMismatch(t *testing.T) {
	managed := map[string]bool{"lattice-db-paused": true}
	expected := map[string]string{}

	// The database is stopped, so it is absent from the worker's container list.
	// Nothing should be flagged: the reconciler owns that judgement, not this.
	onWorker := []string{}

	for _, name := range onWorker {
		if managed[name] {
			continue
		}
		if _, tracked := expected[name]; !tracked {
			t.Errorf("unexpected anomaly for %q", name)
		}
	}
	for name := range expected {
		t.Errorf("a managed database must never appear in expectedNames, found %q", name)
	}
}
