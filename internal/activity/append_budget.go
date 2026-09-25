package activity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrAppendCapacity = errors.New("activity append capacity exhausted")

// Bytes measure reserved serialized event capacity, not filesystem space or
// caller-owned inputs. Slots cover preparation, waiting, writing and reserved
// completions; a slow writer therefore cannot grow an unbounded producer queue.
type AppendStatistics struct {
	EventLimit      int    `json:"event_limit"`
	ByteLimit       int64  `json:"byte_limit"`
	ReservedEvents  int    `json:"reserved_events"`
	ReservedBytes   int64  `json:"reserved_bytes"`
	QueuedEvents    int    `json:"queued_events"`
	PeakEvents      int    `json:"peak_events"`
	PeakBytes       int64  `json:"peak_bytes"`
	CommittedEvents uint64 `json:"committed_events"`
	CancelledEvents uint64 `json:"cancelled_events"`
	RejectedEvents  uint64 `json:"rejected_events"`
	FailedEvents    uint64 `json:"failed_events"`
	QueueWaitNS     uint64 `json:"queue_wait_ns"`
	PersistNS       uint64 `json:"persist_ns"`
}

type appendBudget struct {
	mu    sync.Mutex
	stats AppendStatistics
}

// Close releases unused credits. Append consumes one credit whose remaining
// lifetime belongs to the store, including cancellation and partial writes.
type AppendReservation struct {
	mu        sync.Mutex
	store     *Store
	remaining int
}
type appendTicket struct {
	store *Store
	once  sync.Once
}

func (s *Store) ReserveAppend(ctx context.Context, count int) (*AppendReservation, error) {
	if count < 1 || count > 4096 {
		return nil, errors.New("activity reservation requires 1..4096 events")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := &s.appendBudget
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bytes := int64(count) * MaxEventBytes
	if count > b.stats.EventLimit-b.stats.ReservedEvents || bytes > b.stats.ByteLimit-b.stats.ReservedBytes {
		b.stats.RejectedEvents += uint64(count)
		return nil, fmt.Errorf("%w: %d/%d slots and %d/%d bytes reserved", ErrAppendCapacity, b.stats.ReservedEvents, b.stats.EventLimit, b.stats.ReservedBytes, b.stats.ByteLimit)
	}
	b.stats.ReservedEvents += count
	b.stats.ReservedBytes += bytes
	b.stats.PeakEvents = max(b.stats.PeakEvents, b.stats.ReservedEvents)
	b.stats.PeakBytes = max(b.stats.PeakBytes, b.stats.ReservedBytes)
	return &AppendReservation{store: s, remaining: count}, nil
}

func (r *AppendReservation) take() (*appendTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remaining == 0 {
		return nil, errors.New("activity reservation is closed or exhausted")
	}
	r.remaining--
	return &appendTicket{store: r.store}, nil
}

func (r *AppendReservation) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	unused := r.remaining
	r.remaining = 0
	r.mu.Unlock()
	if unused == 0 {
		return
	}
	b := &r.store.appendBudget
	b.mu.Lock()
	b.stats.ReservedEvents -= unused
	b.stats.ReservedBytes -= int64(unused) * MaxEventBytes
	b.mu.Unlock()
}

func (r *AppendReservation) Append(ctx context.Context, event Event) (Event, error) {
	ticket, err := r.take()
	if err != nil {
		return Event{}, err
	}
	prepared, err := r.store.prepareAppend(ctx, event)
	if err != nil {
		ticket.finish(Event{}, err)
		return Event{}, err
	}
	return r.store.enqueueAppend(ctx, prepared, ticket)
}

func (t *appendTicket) finish(event Event, err error) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		b := &t.store.appendBudget
		b.mu.Lock()
		defer b.mu.Unlock()
		b.stats.ReservedEvents--
		b.stats.ReservedBytes -= MaxEventBytes
		if event.Seq > 0 {
			b.stats.CommittedEvents++
		}
		if err != nil {
			if event.Seq == 0 && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				b.stats.CancelledEvents++
			} else {
				b.stats.FailedEvents++
			}
		}
	})
}

func (s *Store) measureAppend(requests []*appendRequest, started time.Time) {
	b := &s.appendBudget
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.PersistNS += uint64(max(0, time.Since(started).Nanoseconds()))
	for _, request := range requests {
		if !request.enqueued.IsZero() {
			b.stats.QueueWaitNS += uint64(max(0, started.Sub(request.enqueued).Nanoseconds()))
		}
	}
}

func (s *Store) AppendStatistics() AppendStatistics {
	s.appendBudget.mu.Lock()
	stats := s.appendBudget.stats
	s.appendBudget.mu.Unlock()
	s.appendQueueMu.Lock()
	stats.QueuedEvents = len(s.appendQueue)
	s.appendQueueMu.Unlock()
	return stats
}

func (s *Store) removeQueued(request *appendRequest) {
	s.appendQueueMu.Lock()
	defer s.appendQueueMu.Unlock()
	for index, candidate := range s.appendQueue {
		if candidate == request {
			copy(s.appendQueue[index:], s.appendQueue[index+1:])
			s.appendQueue[len(s.appendQueue)-1] = nil
			s.appendQueue = s.appendQueue[:len(s.appendQueue)-1]
			return
		}
	}
}
