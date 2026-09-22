package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDisplayPreferencePreservesExplicitFalse(t *testing.T) {
	home := t.TempDir()
	s := NewDisplayPreferences(home, false)
	if s.Snapshot().ChatGPTMCPUIEnabled {
		t.Fatal("legacy disabled preference was lost")
	}
	value := false
	next, err := s.Update(t.Context(), DisplayChange{ExpectedRevision: 1, ChatGPTMCPUIEnabled: &value})
	if err != nil || next.Revision != 2 {
		t.Fatalf("persist same-value explicit selection: %#v %v", next, err)
	}
	if restored := NewDisplayPreferences(home, true).Snapshot(); restored.ChatGPTMCPUIEnabled || restored.Revision != 2 {
		t.Fatalf("explicit false did not override changed legacy defaults: %#v", restored)
	}
	value = true
	if _, err = s.Update(t.Context(), DisplayChange{ExpectedRevision: 1, ChatGPTMCPUIEnabled: &value}); !errors.Is(err, ErrDisplayRevision) {
		t.Fatalf("stale write accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.Update(ctx, DisplayChange{ExpectedRevision: 2, ChatGPTMCPUIEnabled: &value}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write accepted: %v", err)
	}
	called := 0
	s.Subscribe(func() {
		called++
		if !s.Snapshot().ChatGPTMCPUIEnabled {
			t.Error("listener ran before committed snapshot")
		}
	})
	if _, err = s.Update(t.Context(), DisplayChange{ExpectedRevision: 2, ChatGPTMCPUIEnabled: &value}); err != nil || called != 1 {
		t.Fatalf("enable failed: %v, callbacks=%d", err, called)
	}
	if _, err = s.Update(t.Context(), DisplayChange{ExpectedRevision: 3, ChatGPTMCPUIEnabled: &value}); err != nil || called != 1 {
		t.Fatal("unchanged update caused duplicate notification")
	}
}

func TestDisplayPreferenceFailureDoesNotChangePolicy(t *testing.T) {
	home := t.TempDir()
	s := NewDisplayPreferences(home, true)
	if err := os.Mkdir(filepath.Join(home, "display-settings.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	value := false
	if _, err := s.Update(t.Context(), DisplayChange{ExpectedRevision: 1, ChatGPTMCPUIEnabled: &value}); err == nil {
		t.Fatal("failed atomic replacement reported success")
	}
	if current := s.Snapshot(); !current.ChatGPTMCPUIEnabled || current.Revision != 1 {
		t.Fatalf("failed save changed in-memory policy: %#v", current)
	}
}

func TestCorruptDisplayPreferencesPreserved(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "display-settings.json")
	data := []byte(`{"schema_version":1,"revision":1,"chatgpt_mcp_ui_enabled":"false"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewDisplayPreferences(home, true)
	if state := s.Snapshot(); state.ChatGPTMCPUIEnabled || state.Warning == "" {
		t.Fatalf("corrupt optional UI preference was silently accepted: %#v", state)
	}
	value := true
	if _, err := s.Update(t.Context(), DisplayChange{ExpectedRevision: 1, ChatGPTMCPUIEnabled: &value}); err == nil {
		t.Fatal("corrupt user data was overwritten")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(data) {
		t.Fatal("corrupt user file was not preserved")
	}
}
