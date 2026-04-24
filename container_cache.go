package main

import (
	"sync"
	"time"

	"github.com/aidenappl/lattice-api/db"
	"github.com/aidenappl/lattice-api/query"
	"github.com/aidenappl/lattice-api/structs"
)

// containerCache provides a short-lived in-memory cache for container name -> ID
// lookups. This eliminates the N+1 query problem where every heartbeat, log line,
// and status update triggers a GetContainerByName query.
//
// Cache entries expire after 60 seconds. Misses always fall through to DB.
type containerCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	container *structs.Container
	cachedAt  time.Time
}

const cacheTTL = 60 * time.Second

var containerNameCache = &containerCache{
	entries: make(map[string]cacheEntry),
}

// GetContainerByName returns a cached container or falls through to the DB.
func (c *containerCache) GetContainerByName(name string) (*structs.Container, error) {
	c.mu.RLock()
	if entry, ok := c.entries[name]; ok && time.Since(entry.cachedAt) < cacheTTL {
		c.mu.RUnlock()
		return entry.container, nil
	}
	c.mu.RUnlock()

	container, err := query.GetContainerByName(db.DB, name)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[name] = cacheEntry{container: container, cachedAt: time.Now()}
	c.mu.Unlock()

	return container, nil
}

// Invalidate removes a specific name from the cache.
func (c *containerCache) Invalidate(name string) {
	c.mu.Lock()
	delete(c.entries, name)
	c.mu.Unlock()
}

// InvalidateAll clears the entire cache.
func (c *containerCache) InvalidateAll() {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	c.mu.Unlock()
}
