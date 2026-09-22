// Package insertion stores bounded user supplements independently of tool output.
package insertion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

const (
	Lifetime        = 300 * time.Second
	MaxTextBytes    = 8192
	MaxPending      = 16
	MaxPendingBytes = 32768
	MaxRecords      = 512
	MaxStoreBytes   = 8 << 20
)

var (
	ErrConflict = errors.New("insertion submission id already has different content")
	ErrLimit    = errors.New("insertion queue capacity exceeded")
	ErrNotFound = errors.New("insertion not found")
	validID     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
)

type Target struct {
	Owner        string `json:"owner"`
	Conversation string `json:"conversation_id"`
	Task         string `json:"task_id,omitempty"`
	Thread       string `json:"thread_id,omitempty"`
}

type Item struct {
	Target
	ID           string    `json:"insertion_id"`
	SubmissionID string    `json:"submission_id"`
	Sequence     uint64    `json:"sequence"`
	Text         string    `json:"text"`
	Status       string    `json:"status"`
	ExpiredFrom  string    `json:"expired_from,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	CallID       string    `json:"call_id,omitempty"`
	RunID        string    `json:"run_id,omitempty"`
}

type diskState struct {
	SchemaVersion int    `json:"schema_version"`
	Sequence      uint64 `json:"sequence"`
	Items         []Item `json:"items"`
}

type Store struct {
	root string
	run  string
	now  func() time.Time
	mu   sync.Mutex
}

func New(root, run string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	s := &Store{root: root, run: run, now: now}
	err := s.change(context.Background(), func(state *diskState, at time.Time) (bool, error) {
		dirty := expire(state, at)
		for index := range state.Items {
			item := &state.Items[index]
			if item.Status == "reserved" && item.RunID != run {
				item.Status = "delivery_unknown"
				item.UpdatedAt = at
				dirty = true
			}
		}
		return dirty, nil
	})
	return s, err
}

func (s *Store) change(ctx context.Context, fn func(*diskState, time.Time) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	release, err := filelock.Acquire(ctx, filepath.Join(s.root, ".queue.lock"))
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(s.root, "queue.json")
	state := diskState{SchemaVersion: 1, Items: []Item{}}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() > MaxStoreBytes {
			return errors.New("invalid insertion store; original preserved")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("read insertion store; original preserved: %w", err)
		}
		if state.SchemaVersion != 1 || len(state.Items) > MaxRecords {
			return errors.New("unsupported insertion store; original preserved")
		}
		ids := map[string]bool{}
		for _, item := range state.Items {
			if !validID.MatchString(item.ID) || ids[item.ID] || len(item.Text) > MaxTextBytes || item.CreatedAt.IsZero() || !item.ExpiresAt.Equal(item.CreatedAt.Add(Lifetime)) {
				return errors.New("invalid insertion record; original preserved")
			}
			ids[item.ID] = true
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dirty, err := fn(&state, s.now().UTC())
	if err != nil || !dirty {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > MaxStoreBytes {
		return ErrLimit
	}
	return atomicfile.Write(path, append(data, '\n'), 0600)
}

func waiting(status string) bool { return status == "pending" || status == "target_changed" }
func expire(state *diskState, now time.Time) bool {
	dirty := false
	for index := range state.Items {
		item := &state.Items[index]
		if waiting(item.Status) && !now.Before(item.ExpiresAt) {
			item.ExpiredFrom = item.Status
			item.Status = "expired"
			item.UpdatedAt = now
			dirty = true
		}
	}
	return dirty
}

func (s *Store) Add(ctx context.Context, target Target, submissionID, text string) (Item, error) {
	var result Item
	if !validID.MatchString(target.Conversation) || target.Owner == "" || !validID.MatchString(submissionID) || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > MaxTextBytes {
		return result, errors.New("insertion requires a valid target, submission id and 1–8192 UTF-8 bytes")
	}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := expire(state, now)
		count, bytes := 0, 0
		for _, item := range state.Items {
			if item.Conversation == target.Conversation && item.Owner == target.Owner {
				if item.SubmissionID == submissionID {
					if item.Text != text || item.Task != target.Task || item.Thread != target.Thread {
						return false, ErrConflict
					}
					result = item
					return dirty, nil
				}
				if waiting(item.Status) || item.Status == "reserved" {
					count++
					bytes += len(item.Text)
				}
			}
		}
		if count >= MaxPending || bytes+len(text) > MaxPendingBytes {
			return false, ErrLimit
		}
		kept := make([]Item, 0, len(state.Items))
		for _, item := range state.Items {
			if waiting(item.Status) || item.Status == "reserved" || now.Sub(item.UpdatedAt) < 24*time.Hour {
				kept = append(kept, item)
			}
		}
		state.Items = kept
		if len(state.Items) >= MaxRecords {
			return false, ErrLimit
		}
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return false, err
		}
		state.Sequence++
		result = Item{Target: target, ID: "ins_" + hex.EncodeToString(raw), SubmissionID: submissionID, Sequence: state.Sequence, Text: text, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(Lifetime), UpdatedAt: now}
		state.Items = append(state.Items, result)
		return true, nil
	})
	return result, err
}

// Reserve is called at the first validated root admission, before executing the
// tool. Calls that started before Send never consume a supplement. Admission
// before the deadline remains reserved even when its tool runs past the deadline.
func (s *Store) Reserve(ctx context.Context, target Target, callID string, received time.Time) ([]Item, error) {
	result := []Item{}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := false
		for index := range state.Items {
			item := &state.Items[index]
			// The authenticated root arrival time is authoritative. A read/sweep
			// may acquire this lock first just after the deadline while that root
			// is still completing attribution. Only a formerly pending item may
			// be claimed in that case; paused, cancelled or delivered items cannot.
			claimable := item.Status == "pending" || item.Status == "expired" && item.ExpiredFrom == "pending" && item.CallID == ""
			if !claimable || item.Owner != target.Owner || item.Conversation != target.Conversation {
				continue
			}
			if received.Before(item.CreatedAt) {
				continue
			}
			if !received.Before(item.ExpiresAt) {
				if item.Status == "expired" {
					continue
				}
				item.ExpiredFrom = item.Status
				item.Status = "expired"
				item.UpdatedAt = now
				dirty = true
				continue
			}
			if item.Task != target.Task || item.Thread != target.Thread {
				item.Status = "target_changed"
				item.UpdatedAt = now
				dirty = true
				continue
			}
			item.Status = "reserved"
			item.ExpiredFrom = ""
			item.CallID = callID
			item.RunID = s.run
			item.UpdatedAt = now
			dirty = true
			result = append(result, *item)
		}
		return expire(state, now) || dirty, nil
	})
	return result, err
}

func (s *Store) VerifyTarget(ctx context.Context, callID string, target Target) error {
	return s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := false
		for index := range state.Items {
			item := &state.Items[index]
			if item.Status == "reserved" && item.CallID == callID && item.RunID == s.run && item.Target != target {
				item.Status = "target_changed"
				item.CallID = ""
				item.RunID = ""
				item.UpdatedAt = now
				dirty = true
			}
		}
		return expire(state, now) || dirty, nil
	})
}

// Attached means included in a response, never proof that a model read it.
// An uncertain transport result is retained and is never automatically replayed.
func (s *Store) Finish(ctx context.Context, callID string, attached bool) ([]Item, error) {
	result := []Item{}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := false
		for index := range state.Items {
			item := &state.Items[index]
			if item.Status != "reserved" || item.CallID != callID || item.RunID != s.run {
				continue
			}
			if attached {
				item.Status = "attached"
				result = append(result, *item)
			} else {
				item.Status = "delivery_unknown"
			}
			item.UpdatedAt = now
			dirty = true
		}
		return dirty, nil
	})
	return result, err
}

func (s *Store) List(ctx context.Context, owner, conversation string) ([]Item, error) {
	result := []Item{}
	err := s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := expire(state, now)
		for _, item := range state.Items {
			if item.Owner == owner && item.Conversation == conversation {
				copy := item
				copy.Owner = ""
				copy.RunID = ""
				result = append(result, copy)
			}
		}
		return dirty, nil
	})
	return result, err
}

func (s *Store) Cancel(ctx context.Context, owner, conversation, id string, includeReserved bool) error {
	return s.change(ctx, func(state *diskState, now time.Time) (bool, error) {
		dirty := expire(state, now)
		found := id == ""
		for index := range state.Items {
			item := &state.Items[index]
			if item.Owner != owner || item.Conversation != conversation || id != "" && item.ID != id {
				continue
			}
			found = true
			if id != "" && !includeReserved && item.Status == "reserved" {
				return false, errors.New("insertion already reserved by a tool call; withdrawal was not performed")
			}
			if waiting(item.Status) || includeReserved && item.Status == "reserved" || item.Status == "expired" && item.ExpiredFrom == "pending" {
				item.Status = "cancelled"
				item.UpdatedAt = now
				dirty = true
			}
		}
		if !found {
			return false, ErrNotFound
		}
		return dirty, nil
	})
}

func (s *Store) Sweep(ctx context.Context) error {
	return s.change(ctx, func(state *diskState, now time.Time) (bool, error) { return expire(state, now), nil })
}
