package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolOutputValidationAndAtomicSettings(t *testing.T) {
	home := t.TempDir()
	store := NewDisplayPreferences(home, true)
	if store.Snapshot().ToolOutput != DefaultToolOutputSettings() {
		t.Fatal("missing defaults")
	}
	for _, limit := range []int{1000, 5001, 100000} {
		before := store.Snapshot()
		output := ToolOutputSettings{Enabled: true, MaxChars: limit}
		next, err := store.Update(t.Context(), DisplayChange{ExpectedRevision: before.Revision, ToolOutput: &output})
		if err != nil || next.ToolOutput != output || !next.ChatGPTMCPUIEnabled {
			t.Fatal(next, err)
		}
		if restored := NewDisplayPreferences(home, false).Snapshot(); restored.ToolOutput != output || !restored.ChatGPTMCPUIEnabled {
			t.Fatal(restored)
		}
		if before.ToolOutput.MaxChars == limit {
			t.Fatal("fixture must change immutable snapshot")
		}
	}
	before := store.Snapshot()
	for _, limit := range []int{-1, 0, 999, 100001} {
		_, err := store.Update(t.Context(), DisplayChange{ExpectedRevision: before.Revision, ToolOutput: &ToolOutputSettings{MaxChars: limit}})
		if !errors.Is(err, ErrToolOutputSettings) || store.Snapshot() != before {
			t.Fatal("invalid setting changed policy", limit, err)
		}
	}
	for _, input := range []string{`null`, `{}`, `{"enabled":true}`, `{"enabled":true,"max_chars":1.5}`, `{"enabled":true,"max_chars":null}`, `{"enabled":"true","max_chars":20000}`} {
		var value ToolOutputSettings
		if json.Unmarshal([]byte(input), &value) == nil {
			t.Fatal("accepted invalid JSON", input)
		}
	}
	path := filepath.Join(home, "display-settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := store.Update(t.Context(), DisplayChange{ExpectedRevision: before.Revision, ToolOutput: &ToolOutputSettings{Enabled: false, MaxChars: 20000}})
	if err == nil || store.Snapshot() != before {
		t.Fatal("failed disk replacement changed policy")
	}
}

func TestToolOutputOldAndDamagedConfig(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":1,"revision":7,"chatgpt_mcp_ui_enabled":false}`,
		`{"schema_version":2,"revision":7,"chatgpt_mcp_ui_enabled":false,"tool_output":{"enabled":true,"max_chars":999}}`,
	} {
		home := t.TempDir()
		path := filepath.Join(home, "display-settings.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		store := NewDisplayPreferences(home, true)
		if value := store.Snapshot(); value.ToolOutput != DefaultToolOutputSettings() || value.ChatGPTMCPUIEnabled || value.Revision != 7 {
			t.Fatal(value)
		}
		data, _ := os.ReadFile(path)
		if string(data) != raw {
			t.Fatal("loading rewrote original configuration")
		}
		if strings.Contains(raw, "tool_output") && store.Snapshot().Warning == "" {
			t.Fatal("invalid output settings need visible warning")
		}
	}
}
