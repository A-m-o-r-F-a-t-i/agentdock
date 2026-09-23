// Package snapshot coalesces bounded, immutable read-only builds. Request
// identity, permissions and consumable user messages must never be cached here.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrClosed = errors.New("snapshot cache is closed")

type Info struct {
	CacheHit    bool    `json:"cache_hit"`
	SharedBuild bool    `json:"shared_build"`
	BuildID     uint64  `json:"build_id"`
	WaitMS      float64 `json:"shared_wait_ms"`
	// Optional I/O count for the referenced build, not each waiter or cache hit.
	DocumentReads *int `json:"build_document_reads,omitempty"`
}
type Stats struct {
	Builds   uint64 `json:"builds"`
	Hits     uint64 `json:"hits"`
	Shared   uint64 `json:"shared"`
	InFlight int    `json:"in_flight"`
	Entries  int    `json:"entries"`
}
type entry[T any] struct {
	value   T
	expires time.Time
	used    uint64
	buildID uint64
}
type flight[T any] struct {
	done       chan struct{}
	cancel     context.CancelFunc
	waiters    int
	id         uint64
	generation uint64
	value      T
	err        error
	finished   bool
}
type Cache[T any] struct {
	mu         sync.Mutex
	entries    map[string]*entry[T]
	flights    map[string]*flight[T]
	changed    chan struct{}
	capacity   int
	maxFlights int
	budget     time.Duration
	ttl        time.Duration
	sequence   uint64
	generation uint64
	hits       uint64
	shared     uint64
	closed     bool
}

func New[T any](capacity int, budget, ttl time.Duration) *Cache[T] {
	if capacity < 1 {
		capacity = 1
	}
	if budget <= 0 {
		budget = 5 * time.Second
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &Cache[T]{entries: map[string]*entry[T]{}, flights: map[string]*flight[T]{}, changed: make(chan struct{}), capacity: capacity, maxFlights: 32, budget: budget, ttl: ttl}
}

// Get shares only the build. Every waiter keeps its own cancellation, timing
// and caller identity. The first waiter cannot cancel work needed by others.
func (c *Cache[T]) Get(ctx context.Context, key string, build func(context.Context) (T, error)) (T, Info, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, Info{}, err
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return zero, Info{}, ErrClosed
		}
		if e, ok := c.entries[key]; ok && time.Now().Before(e.expires) {
			c.hits++
			e.used = c.hits + c.sequence
			value, id := e.value, e.buildID
			c.mu.Unlock()
			return value, Info{CacheHit: true, BuildID: id}, nil
		}
		f, exists := c.flights[key]
		if !exists && len(c.flights) >= c.maxFlights {
			changed := c.changed
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return zero, Info{}, ctx.Err()
			case <-changed:
				continue
			}
		}
		if !exists {
			c.sequence++
			buildCtx, cancel := context.WithTimeout(context.Background(), c.budget)
			f = &flight[T]{done: make(chan struct{}), cancel: cancel, id: c.sequence, generation: c.generation}
			c.flights[key] = f
			// Only one bounded goroutine exists for this key until the builder exits,
			// including after its last waiter has cancelled.
			go c.run(buildCtx, key, f, build)
		} else {
			c.shared++
		}
		f.waiters++
		c.mu.Unlock()
		start := time.Now()
		select {
		case <-ctx.Done():
			c.leave(f)
			return zero, Info{SharedBuild: exists, BuildID: f.id, WaitMS: float64(time.Since(start)) / float64(time.Millisecond)}, ctx.Err()
		case <-f.done:
			c.leave(f)
			return f.value, Info{SharedBuild: exists, BuildID: f.id, WaitMS: float64(time.Since(start)) / float64(time.Millisecond)}, f.err
		}
	}
}
func (c *Cache[T]) leave(f *flight[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f.waiters--
	if f.waiters == 0 && !f.finished {
		f.cancel()
	}
}
func (c *Cache[T]) run(ctx context.Context, key string, f *flight[T], build func(context.Context) (T, error)) {
	var value T
	var err error
	func() {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("snapshot builder panicked: %v", v)
			}
		}()
		value, err = build(ctx)
	}()
	if err == nil {
		err = ctx.Err()
	}
	f.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	f.value, f.err, f.finished = value, err, true
	if err == nil && !c.closed && f.generation == c.generation {
		for len(c.entries) >= c.capacity {
			oldKey := ""
			var oldest uint64 = ^uint64(0)
			for k, e := range c.entries {
				if e.used < oldest {
					oldKey, oldest = k, e.used
				}
			}
			delete(c.entries, oldKey)
		}
		c.entries[key] = &entry[T]{value: value, expires: time.Now().Add(c.ttl), used: c.hits + c.sequence, buildID: f.id}
	}
	delete(c.flights, key)
	close(f.done)
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *Cache[T]) Invalidate() { c.mu.Lock(); defer c.mu.Unlock(); c.generation++; clear(c.entries) }
func (c *Cache[T]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Builds: c.sequence, Hits: c.hits, Shared: c.shared, InFlight: len(c.flights), Entries: len(c.entries)}
}
func (c *Cache[T]) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	clear(c.entries)
	for _, f := range c.flights {
		f.cancel()
	}
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *Cache[T]) Wait(ctx context.Context) error {
	for {
		c.mu.Lock()
		if len(c.flights) == 0 {
			c.mu.Unlock()
			return nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
