package main

import (
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/logger"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
)

// containerCache provides a short-lived in-memory cache for container name -> ID
// lookups. This eliminates the N+1 query problem where every heartbeat, log line,
// and status update triggers a GetContainerByName query.
//
// Cache entries expire after 60 seconds. Misses always fall through to DB,
// except on the log path — see LookupForLog.
type containerCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	// unmanaged remembers names confirmed absent, for LookupForLog only.
	unmanaged map[string]time.Time
	fetch     func(name string) (*structs.Container, error)
}

type cacheEntry struct {
	container *structs.Container
	cachedAt  time.Time
}

const cacheTTL = 60 * time.Second

// unmanagedTTL is how long LookupForLog remembers a name with no Lattice
// container. Runners stream logs for every container on the host, managed or
// not, so an unmanaged one otherwise costs a DB query and a warning per line.
// Nothing is lost inside the window: log rows keep their container_name, and
// ListContainerLogs matches on it when container_id is NULL.
const unmanagedTTL = 5 * time.Minute

// errUnmanagedContainer is a remembered miss, so callers can tell it from a
// fresh one — only the fresh one is worth a warning.
var errUnmanagedContainer = errors.New("container name is not managed by lattice")

var containerNameCache = newContainerCache(func(name string) (*structs.Container, error) {
	return query.GetContainerByName(db.DB, name)
})

func newContainerCache(fetch func(name string) (*structs.Container, error)) *containerCache {
	return &containerCache{
		entries:   make(map[string]cacheEntry),
		unmanaged: make(map[string]time.Time),
		fetch:     fetch,
	}
}

// GetContainerByName returns a cached container or falls through to the DB.
func (c *containerCache) GetContainerByName(name string) (*structs.Container, error) {
	c.mu.RLock()
	if entry, ok := c.entries[name]; ok && time.Since(entry.cachedAt) < cacheTTL {
		c.mu.RUnlock()
		return entry.container, nil
	}
	c.mu.RUnlock()

	container, err := c.fetch(name)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[name] = cacheEntry{container: container, cachedAt: time.Now()}
	delete(c.unmanaged, name)
	c.mu.Unlock()

	return container, nil
}

// LookupForLog is GetContainerByName for the container-log path, which also
// remembers misses for unmanagedTTL and returns errUnmanagedContainer for
// them. The status and heartbeat paths must not use it: a container created
// inside the window would have its state sync skipped.
func (c *containerCache) LookupForLog(name string) (*structs.Container, error) {
	c.mu.RLock()
	seen, ok := c.unmanaged[name]
	c.mu.RUnlock()
	if ok && time.Since(seen) < unmanagedTTL {
		return nil, errUnmanagedContainer
	}

	container, err := c.GetContainerByName(name)
	if errors.Is(err, sql.ErrNoRows) {
		c.mu.Lock()
		c.unmanaged[name] = time.Now()
		c.mu.Unlock()
	}
	return container, err
}

// Invalidate removes a specific name from the cache.
func (c *containerCache) Invalidate(name string) {
	c.mu.Lock()
	delete(c.entries, name)
	delete(c.unmanaged, name)
	c.mu.Unlock()
}

// InvalidateAll clears the entire cache.
func (c *containerCache) InvalidateAll() {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	c.unmanaged = make(map[string]time.Time)
	c.mu.Unlock()
}

// evictExpired removes all entries whose TTL has elapsed. Without this, the map
// grows unbounded as containers come and go — expired entries are only replaced
// on a fresh lookup of the same name, never on a name that is never queried again.
func (c *containerCache) evictExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, entry := range c.entries {
		if time.Since(entry.cachedAt) >= cacheTTL {
			delete(c.entries, name)
		}
	}
	for name, seen := range c.unmanaged {
		if time.Since(seen) >= unmanagedTTL {
			delete(c.unmanaged, name)
		}
	}
}

// StartEviction launches a background goroutine that periodically prunes expired
// cache entries, bounding memory over the lifetime of the process.
func (c *containerCache) StartEviction() {
	go func() {
		defer logger.Recover("container-cache-eviction")
		ticker := time.NewTicker(cacheTTL)
		defer ticker.Stop()
		for range ticker.C {
			c.evictExpired()
		}
	}()
}
