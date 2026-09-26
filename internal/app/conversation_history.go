package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	sidebarHistoryCapacity = 128
	sidebarHistoryIDBudget = 1_000_000
	sidebarHistoryTTL      = 15 * time.Minute
	sidebarUnattributedKey = "kind:unattributed"
)

type sidebarHistorySnapshot struct {
	scope string
	ids   []string
	used  time.Time
}

// The cache freezes only ordering IDs. Visibility, titles, permissions and
// running state are always projected again from the current authoritative data.
type sidebarHistoryCache struct {
	mu      sync.Mutex
	entries map[string]sidebarHistorySnapshot
	ids     int
}

func (cache *sidebarHistoryCache) order(scope, cursor string, current []ConversationItem, now time.Time) (ordered, arrivals []ConversationItem, token string, reset bool, err error) {
	keys := make([]string, len(current))
	seen := make(map[string]struct{}, len(current))
	for index, item := range current {
		key, keyErr := sidebarNavigationKey(item)
		if keyErr != nil {
			return nil, nil, "", false, keyErr
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, nil, "", false, fmt.Errorf("SIDEBAR_NAVIGATION_KEY_DUPLICATE: %s", key)
		}
		seen[key] = struct{}{}
		keys[index] = key
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = map[string]sidebarHistorySnapshot{}
	}
	for id, entry := range cache.entries {
		if now.Sub(entry.used) >= sidebarHistoryTTL {
			cache.ids -= len(entry.ids)
			delete(cache.entries, id)
		}
	}
	entry, exists := cache.entries[cursor]
	if exists && entry.scope != scope {
		return nil, nil, "", false, errors.New("sidebar history cursor belongs to another project or filter")
	}
	reset = cursor != "" && !exists
	if !exists {
		if len(current) > sidebarHistoryIDBudget {
			return nil, nil, "", false, errors.New("sidebar history exceeds the bounded snapshot capacity; narrow the search")
		}
		for len(cache.entries) >= sidebarHistoryCapacity || cache.ids+len(current) > sidebarHistoryIDBudget {
			oldest := ""
			var at time.Time
			for id, candidate := range cache.entries {
				if oldest == "" || candidate.used.Before(at) {
					oldest, at = id, candidate.used
				}
			}
			cache.ids -= len(cache.entries[oldest].ids)
			delete(cache.entries, oldest)
		}
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			return nil, nil, "", false, err
		}
		cursor = hex.EncodeToString(value[:])
		entry = sidebarHistorySnapshot{scope: scope, ids: make([]string, 0, len(current))}
		for _, key := range keys {
			entry.ids = append(entry.ids, key)
		}
		cache.ids += len(entry.ids)
	}
	entry.used = now
	cache.entries[cursor] = entry
	byID := make(map[string]ConversationItem, len(current))
	for index, item := range current {
		byID[keys[index]] = item
	}
	ordered = make([]ConversationItem, 0, len(entry.ids))
	for _, id := range entry.ids {
		if item, visible := byID[id]; visible {
			ordered = append(ordered, item)
			delete(byID, id)
		}
	}
	// New conversations are merged separately, so moving live activity cannot
	// push a previously visible history row across the frozen page boundary.
	arrivals = make([]ConversationItem, 0, len(byID))
	for index, item := range current {
		if _, fresh := byID[keys[index]]; fresh {
			arrivals = append(arrivals, item)
		}
	}
	return ordered, arrivals, cursor, reset, nil
}

func sidebarNavigationKey(item ConversationItem) (string, error) {
	if item.IsUnattributed {
		if item.ID != "" || conversationWorkspace(item) != "unattributed" {
			return "", errors.New("SIDEBAR_UNATTRIBUTED_IDENTITY_INVALID")
		}
		return sidebarUnattributedKey, nil
	}
	if item.ID == "" {
		return "", errors.New("SIDEBAR_CONVERSATION_ID_MISSING")
	}
	if item.ID == "unattributed" || strings.HasPrefix(item.ID, "footer:") {
		return "", errors.New("SIDEBAR_RESERVED_KEY")
	}
	return item.ID, nil
}
