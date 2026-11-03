package main

import (
	"context"
	"testing"

	"github.com/aidenappl/lattice-api/structs"
)

// APL-1: instance 12 ("lattice-db-NexusDB") must not be matched to the
// container instance 11 left behind under the same name.
func TestMatchObservedDatabaseByInstanceID(t *testing.T) {
	inst12 := structs.DatabaseInstance{ID: 12, ContainerName: "lattice-db-NexusDB"}

	tests := []struct {
		name     string
		observed []observedDbContainer
		want     bool
	}{
		{"own labelled container", []observedDbContainer{{ContainerName: "lattice-db-NexusDB", InstanceID: 12}}, true},
		{"same name, another instance's label", []observedDbContainer{{ContainerName: "lattice-db-NexusDB", InstanceID: 11}}, false},
		{"unlabelled legacy container falls back to the name", []observedDbContainer{{ContainerName: "lattice-db-NexusDB"}}, true},
		{"labelled under another name still matches by id", []observedDbContainer{{ContainerName: "lattice-db-renamed", InstanceID: 12}}, true},
		{"nothing there", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := matchObservedDatabase(inst12, tc.observed)
			if ok != tc.want {
				t.Fatalf("matched = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestFindDatabaseOrphans(t *testing.T) {
	instances := []structs.DatabaseInstance{
		{ID: 12, ContainerName: "lattice-db-NexusDB", VolumeName: "lattice-dbdata-NexusDB"},
		{ID: 13, ContainerName: "lattice-db-legacy", VolumeName: "lattice-dbdata-legacy"},
	}
	observed := []observedDbContainer{
		{ContainerName: "lattice-db-NexusDB", InstanceID: 11, State: "running"}, // retired instance's container
		{ContainerName: "lattice-db-NexusDB-new", InstanceID: 12},               // owned by id
		{ContainerName: "lattice-db-legacy"},                                    // owned by name (no label)
		{ContainerName: "lattice-db-stray"},                                     // unlabelled, no instance
	}
	volumes := []string{"lattice-dbdata-NexusDB", "lattice-dbdata-legacy", "lattice-dbdata-gone"}

	got := findDatabaseOrphans(instances, observed, volumes)
	if len(got.Containers) != 2 || got.Containers[0].InstanceID != 11 || got.Containers[1].Name != "lattice-db-stray" {
		t.Fatalf("containers = %+v", got.Containers)
	}
	if len(got.Volumes) != 1 || got.Volumes[0] != "lattice-dbdata-gone" {
		t.Fatalf("volumes = %v", got.Volumes)
	}
	if !got.VolumesKnown {
		t.Fatal("volumes were reported, so they are known")
	}

	if old := findDatabaseOrphans(instances, nil, nil); old.VolumesKnown || len(old.Volumes) != 0 {
		t.Fatalf("a runner that sends no volumes must not report any: %+v", old)
	}
}

// A failed create must stay in error even when a running container is observed
// for it. Returning before the lifecycle is touched is the assertion: the
// lifecycle needs the database, which tests don't have, so a regression panics.
func TestReconcileNeverPromotesFailedCreate(t *testing.T) {
	instance := structs.DatabaseInstance{
		ID:            12,
		ContainerName: "lattice-db-NexusDB",
		Status:        string(structs.DBStatusError),
		LastError:     &structs.DatabaseError{Code: structs.DBErrCodeCreateFailed, Message: `data volume "lattice-dbdata-NexusDB" already exists`},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reconciler tried to transition a failed create: %v", r)
		}
	}()
	reconcileDatabaseInstance(context.Background(), instance,
		observedDbContainer{ContainerName: "lattice-db-NexusDB", State: "running", Health: "healthy"}, true)
}
