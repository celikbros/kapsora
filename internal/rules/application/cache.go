package application

import (
	"sync"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/rules/engine"
)

// DefaultCacheSize is how many compiled published versions one process keeps. A tenant has
// a handful of rule sets and a handful of live versions each, so this is generous; it
// exists so a long-lived process cannot grow without bound after years of revisions.
const DefaultCacheSize = 256

// ProgramCache holds compiled rule set versions, keyed by version id. Only a PUBLISHED
// version is ever put here: a published version never changes, so an entry never needs
// invalidating, and a draft that changed under a cached program would evaluate the rules
// the author had already replaced.
type ProgramCache struct {
	mu      sync.RWMutex
	max     int
	entries map[uuid.UUID]*engine.Program
}

// NewProgramCache returns a cache holding at most max entries; max <= 0 means
// DefaultCacheSize.
func NewProgramCache(max int) *ProgramCache {
	if max <= 0 {
		max = DefaultCacheSize
	}
	return &ProgramCache{max: max, entries: make(map[uuid.UUID]*engine.Program, max)}
}

// Get returns the compiled program of a version, if it is held.
func (c *ProgramCache) Get(versionID uuid.UUID) (*engine.Program, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.entries[versionID]
	return p, ok
}

// Put stores the compiled program of a published version. When the cache is full it is
// emptied rather than evicted one entry at a time: recompiling is cheap and deterministic,
// and a least-recently-used list would be more machinery than the problem deserves.
func (c *ProgramCache) Put(versionID uuid.UUID, p *engine.Program) {
	if c == nil || p == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		c.entries = make(map[uuid.UUID]*engine.Program, c.max)
	}
	c.entries[versionID] = p
}

// Len is how many programs are held; it exists for the tests and for a health readout.
func (c *ProgramCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
