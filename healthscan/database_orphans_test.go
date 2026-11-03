package healthscan

import (
	"testing"
	"time"
)

func TestDatabaseOrphanAnomalies(t *testing.T) {
	now := time.Now()
	o := workerDatabaseOrphans{
		DatabaseOrphans: DatabaseOrphans{
			Containers: []OrphanDatabaseContainer{
				{Name: "lattice-db-NexusDB", InstanceID: 11, State: "running"},
				{Name: "lattice-db-stray", State: "exited"},
			},
			Volumes:      []string{"lattice-dbdata-gone"},
			VolumesKnown: true,
		},
		UpdatedAt: now.Add(-30 * time.Second),
	}

	got := databaseOrphanAnomalies(4, "worker-4", o, now)
	if len(got) != 3 {
		t.Fatalf("got %d anomalies, want 3: %+v", len(got), got)
	}
	if got[0].Type != AnomalyOrphanedDatabase || got[0].Details["database_instance_id"] != 11 ||
		got[0].ID != "4:orphaned_database:lattice-db-NexusDB" {
		t.Fatalf("labelled orphan: %+v", got[0])
	}
	if _, ok := got[1].Details["database_instance_id"]; ok {
		t.Fatalf("unlabelled orphan names an instance: %+v", got[1])
	}
	if got[2].Type != AnomalyOrphanedDatabaseVolume || got[2].Details["volume_name"] != "lattice-dbdata-gone" {
		t.Fatalf("volume orphan: %+v", got[2])
	}

	// A report from a worker that has gone quiet is not evidence of anything now.
	o.UpdatedAt = now.Add(-dbOrphanFreshness - time.Second)
	if stale := databaseOrphanAnomalies(4, "worker-4", o, now); len(stale) != 0 {
		t.Fatalf("stale report produced %d anomalies", len(stale))
	}
}
