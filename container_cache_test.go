package main

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/structs"
)

// fakeContainers answers lookups from a map and counts how often it is asked,
// standing in for query.GetContainerByName. Misses are wrapped the way the
// real query wraps them, so errors.Is(err, sql.ErrNoRows) is what is tested.
type fakeContainers struct {
	byName map[string]*structs.Container
	calls  map[string]int
}

func (f *fakeContainers) fetch(name string) (*structs.Container, error) {
	f.calls[name]++
	if c, ok := f.byName[name]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("failed to scan container: %w", sql.ErrNoRows)
}

func newFakeCache(managed ...string) (*containerCache, *fakeContainers) {
	f := &fakeContainers{byName: map[string]*structs.Container{}, calls: map[string]int{}}
	for i, name := range managed {
		f.byName[name] = &structs.Container{ID: i + 1, Name: name}
	}
	return newContainerCache(f.fetch), f
}

// Runners stream logs for every container on the host. An unmanaged one used
// to cost a DB query and a warning per log line — 8k+ warnings a day.
func TestLookupForLogRemembersUnmanagedNames(t *testing.T) {
	c, f := newFakeCache("managed")

	_, err := c.LookupForLog("openbucket")
	if err == nil || errors.Is(err, errUnmanagedContainer) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("first miss: got %v, want the fresh sql.ErrNoRows so the caller warns once", err)
	}
	for i := range 50 {
		if _, err := c.LookupForLog("openbucket"); !errors.Is(err, errUnmanagedContainer) {
			t.Fatalf("repeat miss %d: got %v, want errUnmanagedContainer", i, err)
		}
	}
	if got := f.calls["openbucket"]; got != 1 {
		t.Errorf("DB queried %d times for an unmanaged name, want 1", got)
	}
}

func TestLookupForLog(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(c *containerCache)
		lookup  string
		wantID  int
		wantErr error
	}{
		{
			name:   "managed container resolves",
			lookup: "managed",
			wantID: 1,
		},
		{
			name:    "fresh miss is the DB error",
			lookup:  "ghost",
			wantErr: sql.ErrNoRows,
		},
		{
			name: "remembered miss expires after unmanagedTTL",
			prepare: func(c *containerCache) {
				c.unmanaged["ghost"] = time.Now().Add(-unmanagedTTL - time.Second)
			},
			lookup:  "ghost",
			wantErr: sql.ErrNoRows,
		},
		{
			name: "Invalidate forgets a remembered miss",
			prepare: func(c *containerCache) {
				c.unmanaged["ghost"] = time.Now()
				c.Invalidate("ghost")
			},
			lookup:  "ghost",
			wantErr: sql.ErrNoRows,
		},
		{
			name: "InvalidateAll forgets remembered misses",
			prepare: func(c *containerCache) {
				c.unmanaged["ghost"] = time.Now()
				c.InvalidateAll()
			},
			lookup:  "ghost",
			wantErr: sql.ErrNoRows,
		},
		{
			// A name remembered as unmanaged that later resolves (created by a
			// deploy) must not stay shadowed once the DB answers for it.
			name: "a hit clears the remembered miss",
			prepare: func(c *containerCache) {
				c.unmanaged["managed"] = time.Now().Add(-unmanagedTTL - time.Second)
			},
			lookup: "managed",
			wantID: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newFakeCache("managed")
			if tt.prepare != nil {
				tt.prepare(c)
			}
			got, err := c.LookupForLog(tt.lookup)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.ID != tt.wantID {
				t.Errorf("ID = %d, want %d", got.ID, tt.wantID)
			}
			if _, stillRemembered := c.unmanaged[tt.lookup]; stillRemembered {
				t.Errorf("%q still remembered as unmanaged after resolving", tt.lookup)
			}
		})
	}
}

// The status and heartbeat paths use GetContainerByName, which must keep
// falling through on a miss: a container deployed inside the window would
// otherwise have its state sync skipped.
func TestGetContainerByNameDoesNotRememberMisses(t *testing.T) {
	c, f := newFakeCache()
	for i := range 3 {
		if _, err := c.GetContainerByName("new-deploy"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("lookup %d: got %v, want sql.ErrNoRows", i, err)
		}
	}
	if got := f.calls["new-deploy"]; got != 3 {
		t.Errorf("DB queried %d times, want 3 — misses must not be cached on this path", got)
	}
	if _, remembered := c.unmanaged["new-deploy"]; remembered {
		t.Error("GetContainerByName recorded a miss in unmanaged")
	}
}

func TestEvictExpiredPrunesUnmanaged(t *testing.T) {
	c, _ := newFakeCache()
	c.unmanaged["old"] = time.Now().Add(-unmanagedTTL - time.Second)
	c.unmanaged["recent"] = time.Now()
	c.evictExpired()
	if _, ok := c.unmanaged["old"]; ok {
		t.Error("expired unmanaged entry was not pruned")
	}
	if _, ok := c.unmanaged["recent"]; !ok {
		t.Error("live unmanaged entry was pruned")
	}
}
