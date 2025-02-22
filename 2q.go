package lru

import (
	"fmt"
	"sync"

	"github.com/descope/golang-lru/simplelru"
)

const (
	// Default2QRecentRatio is the ratio of the 2Q cache dedicated
	// to recently added entries that have only been accessed once.
	Default2QRecentRatio = 0.25

	// Default2QGhostEntries is the default ratio of ghost
	// entries kept to track entries recently evicted
	Default2QGhostEntries = 0.50
)

// TwoQueueCache is a thread-safe fixed size 2Q cache.
// 2Q is an enhancement over the standard LRU cache
// in that it tracks both frequently and recently used
// entries separately. This avoids a burst in access to new
// entries from evicting frequently used entries. It adds some
// additional tracking overhead to the standard LRU cache, and is
// computationally about 2x the cost, and adds some metadata over
// head. The ARCCache is similar, but does not require setting any
// parameters.
type TwoQueueCache[K comparable, T any] struct {
	size       int
	recentSize int

	recent      simplelru.LRUCache[K, T]
	frequent    simplelru.LRUCache[K, T]
	recentEvict simplelru.LRUCache[K, T]
	lock        sync.RWMutex
}

// New2Q creates a new TwoQueueCache using the default
// values for the parameters.
func New2Q[K comparable, T any](size int) (*TwoQueueCache[K, T], error) {
	return New2QParams[K, T](size, Default2QRecentRatio, Default2QGhostEntries)
}

// New2QParams creates a new TwoQueueCache using the provided
// parameter values.
func New2QParams[K comparable, T any](size int, recentRatio, ghostRatio float64) (*TwoQueueCache[K, T], error) {
	if size <= 0 {
		return nil, fmt.Errorf("invalid size")
	}
	if recentRatio < 0.0 || recentRatio > 1.0 {
		return nil, fmt.Errorf("invalid recent ratio")
	}
	if ghostRatio < 0.0 || ghostRatio > 1.0 {
		return nil, fmt.Errorf("invalid ghost ratio")
	}

	// Determine the sub-sizes
	recentSize := int(float64(size) * recentRatio)
	evictSize := int(float64(size) * ghostRatio)

	// Allocate the LRUs
	recent, err := simplelru.NewLRU[K, T](size, nil)
	if err != nil {
		return nil, err
	}
	frequent, err := simplelru.NewLRU[K, T](size, nil)
	if err != nil {
		return nil, err
	}
	recentEvict, err := simplelru.NewLRU[K, T](evictSize, nil)
	if err != nil {
		return nil, err
	}

	// Initialize the cache
	c := &TwoQueueCache[K, T]{
		size:        size,
		recentSize:  recentSize,
		recent:      recent,
		frequent:    frequent,
		recentEvict: recentEvict,
	}
	return c, nil
}

// Get looks up a key's value from the cache.
func (c *TwoQueueCache[K, T]) Get(key K) (value T, ok bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Check if this is a frequent value
	if val, ok := c.frequent.Get(key); ok {
		return val, ok
	}

	// If the value is contained in recent, then we
	// promote it to frequent
	if val, ok := c.recent.Peek(key); ok {
		c.recent.Remove(key)
		c.frequent.Add(key, val)
		return val, ok
	}

	// No hit
	var emptyV T
	return emptyV, false
}

// Add adds a value to the cache.
func (c *TwoQueueCache[K, T]) Add(key K, value T) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Check if the value is frequently used already,
	// and just update the value
	if c.frequent.Contains(key) {
		c.frequent.Add(key, value)
		return
	}

	// Check if the value is recently used, and promote
	// the value into the frequent list
	if c.recent.Contains(key) {
		c.recent.Remove(key)
		c.frequent.Add(key, value)
		return
	}

	// If the value was recently evicted, add it to the
	// frequently used list
	if c.recentEvict.Contains(key) {
		c.ensureSpace(true)
		c.recentEvict.Remove(key)
		c.frequent.Add(key, value)
		return
	}

	// Add to the recently seen list
	c.ensureSpace(false)
	c.recent.Add(key, value)
}

// ensureSpace is used to ensure we have space in the cache
func (c *TwoQueueCache[K, T]) ensureSpace(recentEvict bool) {
	// If we have space, nothing to do
	recentLen := c.recent.Len()
	freqLen := c.frequent.Len()
	if recentLen+freqLen < c.size {
		return
	}

	// If the recent buffer is larger than
	// the target, evict from there
	if recentLen > 0 && (recentLen > c.recentSize || (recentLen == c.recentSize && !recentEvict)) {
		k, _, _ := c.recent.RemoveOldest()
		var emptyV T
		c.recentEvict.Add(k, emptyV)
		return
	}

	// Remove from the frequent list otherwise
	c.frequent.RemoveOldest()
}

// Len returns the number of items in the cache.
func (c *TwoQueueCache[K, T]) Len() int {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.recent.Len() + c.frequent.Len()
}

// Keys returns a slice of the keys in the cache.
// The frequently used keys are first in the returned slice.
func (c *TwoQueueCache[K, T]) Keys() []K {
	c.lock.RLock()
	defer c.lock.RUnlock()
	k1 := c.frequent.Keys()
	k2 := c.recent.Keys()
	return append(k1, k2...)
}

// Remove removes the provided key from the cache.
func (c *TwoQueueCache[K, T]) Remove(key K) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.frequent.Remove(key) {
		return
	}
	if c.recent.Remove(key) {
		return
	}
	if c.recentEvict.Remove(key) {
		return
	}
}

// Purge is used to completely clear the cache.
func (c *TwoQueueCache[K, T]) Purge() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.recent.Purge()
	c.frequent.Purge()
	c.recentEvict.Purge()
}

// Contains is used to check if the cache contains a key
// without updating recency or frequency.
func (c *TwoQueueCache[K, T]) Contains(key K) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.frequent.Contains(key) || c.recent.Contains(key)
}

// Peek is used to inspect the cache value of a key
// without updating recency or frequency.
func (c *TwoQueueCache[K, T]) Peek(key K) (value T, ok bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if val, ok := c.frequent.Peek(key); ok {
		return val, ok
	}
	return c.recent.Peek(key)
}
