package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

var ErrDisplayRevision = errors.New("display settings changed; refresh before saving")

type DisplaySettings struct {
	SchemaVersion       int    `json:"schema_version"`
	Revision            uint64 `json:"revision"`
	ChatGPTMCPUIEnabled bool   `json:"chatgpt_mcp_ui_enabled"`
	Warning             string `json:"warning,omitempty"`
	WarningCode         string `json:"warning_code,omitempty"`
	WarningDetail       string `json:"warning_detail,omitempty"`
}

type DisplayChange struct {
	ExpectedRevision    uint64 `json:"expected_revision"`
	ChatGPTMCPUIEnabled *bool  `json:"chatgpt_mcp_ui_enabled"`
}

// DisplayPreferences publishes immutable snapshots. Cosmetic writes never
// mutate the execution configuration or restart the running service.
type DisplayPreferences struct {
	mu        sync.Mutex
	path      string
	current   atomic.Pointer[DisplaySettings]
	listeners []func()
	loadError error
	persisted bool
}

func NewDisplayPreferences(home string, legacyEnabled bool) *DisplayPreferences {
	store := &DisplayPreferences{path: filepath.Join(home, "display-settings.json")}
	settings := DisplaySettings{SchemaVersion: 1, Revision: 1, ChatGPTMCPUIEnabled: legacyEnabled}
	file, err := os.Open(store.path)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(file, 4097))
		err = errors.Join(readErr, file.Close())
		if err == nil && len(data) > 4096 {
			err = errors.New("display settings exceed 4096 bytes")
		}
		if err == nil {
			var disk struct {
				SchemaVersion int    `json:"schema_version"`
				Revision      uint64 `json:"revision"`
				Enabled       *bool  `json:"chatgpt_mcp_ui_enabled"`
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&disk)
			var extra any
			if err == nil && decoder.Decode(&extra) != io.EOF {
				err = errors.New("display settings must contain one JSON object")
			}
			if err == nil && (disk.SchemaVersion != 1 || disk.Revision == 0 || disk.Enabled == nil) {
				err = errors.New("invalid display settings schema")
			}
			if err == nil {
				settings = DisplaySettings{SchemaVersion: 1, Revision: disk.Revision, ChatGPTMCPUIEnabled: *disk.Enabled}
				store.persisted = true
			}
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Preserve corrupt preferences and suppress only optional UI. Core tool
		// execution must remain available so the user can repair the file.
		store.loadError = err
		settings.ChatGPTMCPUIEnabled = false
		settings.Warning = "Display preferences could not be loaded; the original file was preserved. " + err.Error()
		settings.WarningCode = "display_preferences_load_failed"
		settings.WarningDetail = err.Error()
	}
	store.current.Store(&settings)
	return store
}

func (s *DisplayPreferences) Snapshot() DisplaySettings { return *s.current.Load() }

func (s *DisplayPreferences) Subscribe(listener func()) {
	if listener == nil {
		return
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, listener)
	s.mu.Unlock()
}

func (s *DisplayPreferences) Update(ctx context.Context, change DisplayChange) (DisplaySettings, error) {
	s.mu.Lock()
	current := s.Snapshot()
	fail := func(err error) (DisplaySettings, error) { s.mu.Unlock(); return current, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if s.loadError != nil {
		return fail(fmt.Errorf("original display settings require repair: %w", s.loadError))
	}
	if change.ChatGPTMCPUIEnabled == nil {
		return fail(errors.New("chatgpt_mcp_ui_enabled is required"))
	}
	if change.ExpectedRevision != current.Revision {
		return fail(ErrDisplayRevision)
	}
	if s.persisted && current.ChatGPTMCPUIEnabled == *change.ChatGPTMCPUIEnabled {
		s.mu.Unlock()
		return current, nil
	}
	next := DisplaySettings{SchemaVersion: 1, Revision: current.Revision + 1, ChatGPTMCPUIEnabled: *change.ChatGPTMCPUIEnabled}
	if next.Revision == 0 {
		return fail(errors.New("display revision overflow"))
	}
	data, err := json.Marshal(next)
	if err != nil {
		return fail(err)
	}
	if err = atomicfile.Write(s.path, append(data, '\n'), 0o600); err != nil {
		return fail(err)
	}
	s.current.Store(&next)
	s.persisted = true
	listeners := append([]func(){}, s.listeners...)
	s.mu.Unlock()
	// Observers re-read the current snapshot and serialize their catalog changes.
	for _, listener := range listeners {
		listener()
	}
	return next, nil
}
