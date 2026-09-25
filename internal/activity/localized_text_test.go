package activity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalizedTextPersistsRedactsAndClearsOnNewSummary(t *testing.T) {
	root := t.TempDir()
	secret := "localized-fixture-secret"
	store, err := New(root, Options{}, secret)
	if err != nil {
		t.Fatal(err)
	}
	raw := "读取文件 · " + secret
	event := Event{Binding: Binding{CallID: "call_1234567890abcdef1234567890abcdef", ConversationID: "conv_1234567890abcdef1234567890abcdef"}, Kind: "call.created", ToolName: "read_file", Title: raw, Summary: raw, LabelSource: "tool", TitleText: NewLocalizedText("tool.read_file", raw, " · "+secret), SummaryText: NewLocalizedText("tool.read_file", raw, " · "+secret)}
	saved, err := store.Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if saved.TitleText == nil || !saved.TitleText.valid(saved.Title) || saved.SummaryText == nil || !saved.SummaryText.valid(saved.Summary) {
		t.Fatal("redaction detached descriptor from retained text")
	}
	encoded, err := json.Marshal(saved)
	if err != nil || strings.Contains(string(encoded), secret) {
		t.Fatal("raw secret survived in presentation metadata", err)
	}
	for n := 0; n < 2; n++ {
		reopened, err := New(root, Options{}, secret)
		if err != nil {
			t.Fatal(err)
		}
		call, err := reopened.Call(context.Background(), "call_1234567890abcdef1234567890abcdef")
		if err != nil {
			t.Fatal(err)
		}
		if call.TitleText == nil || call.SummaryText == nil || call.LabelSource != "tool" || !call.TitleText.valid(call.Title) {
			t.Fatalf("disk projection lost descriptor: %+v", call)
		}
		call.TitleText.Args[0] = "mutated caller copy"
		again, err := reopened.Call(context.Background(), "call_1234567890abcdef1234567890abcdef")
		if err != nil || again.TitleText.Args[0] == "mutated caller copy" {
			t.Fatal("projection shares mutable descriptor", err)
		}
	}
	failure := "actual failure 原文 한국어"
	_, err = store.Append(context.Background(), Event{Binding: event.Binding, Kind: "call.completed", ToolName: "read_file", Status: "failed", Summary: failure})
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.Call(context.Background(), "call_1234567890abcdef1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if call.Summary != failure || call.SummaryText != nil {
		t.Fatalf("old success descriptor replaced a failure: %+v", call)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), secret) {
			t.Errorf("secret leaked into %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestLocalizedTextRejectsMalformedAndStaleDescriptors(t *testing.T) {
	raw := "读取文件"
	invalid := []*LocalizedText{
		nil, {SchemaVersion: 2, Code: "tool.read_file", Args: []string{}, TextHash: textDigest(raw)},
		{SchemaVersion: 1, Code: "untrusted.tool", Args: []string{}, TextHash: textDigest(raw)},
		{SchemaVersion: 1, Code: "tool.read_file", Args: make([]string, 7), TextHash: textDigest(raw)},
		{SchemaVersion: 1, Code: "tool.read_file", Args: []string{strings.Repeat("x", 1025)}, TextHash: textDigest(raw)},
		{SchemaVersion: 1, Code: "tool.read_file", Args: []string{}, TextHash: textDigest("other")},
	}
	for _, descriptor := range invalid {
		value := NewRedactor().Event(Event{Title: raw, Summary: raw, TitleText: descriptor, SummaryText: descriptor, LabelSource: "user-supplied"})
		if value.TitleText != nil || value.SummaryText != nil || value.Title != raw || value.Summary != raw || value.LabelSource != "" {
			t.Fatal("malformed presentation changed original", value)
		}
	}
}
