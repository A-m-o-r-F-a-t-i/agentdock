package activity

import (
	"context"
	"sync"
)

// resourceMutex hands a token directly to a waiting goroutine. The previous
// TryLock/ticker loop made every journal reader and writer race again after each
// release; under Windows disk load a busy caller could repeatedly overtake an
// older audit append until its unchanged five-second deadline expired.
// This zero-value lock keeps cancellation and the cross-process lock intact,
// without timer polling, background waiters or increasing any execution budget.
type resourceMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *resourceMutex) initialize() {
	m.once.Do(func() {
		m.token = make(chan struct{}, 1)
		m.token <- struct{}{}
	})
}

func (m *resourceMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.initialize()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
	}
	if err := ctx.Err(); err != nil {
		m.Unlock()
		return err
	}
	return nil
}

func (m *resourceMutex) Lock() {
	_ = m.LockContext(context.Background())
}

func (m *resourceMutex) Unlock() {
	m.initialize()
	select {
	case m.token <- struct{}{}:
	default:
		panic("activity: unlock of unlocked resource mutex")
	}
}
