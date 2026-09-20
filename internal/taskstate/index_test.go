package taskstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskIndexBoundedRecoveryAndWarnings(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 14; i++ {
		task, err := s.Create("long recovery title", "goal", []string{"verified"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.UpdateThread(task.ID, "main", "checkpoint", ThreadInput{Summary: strings.Repeat("\"", 7000), NextAction: strings.Repeat("\"", 7000), WorkspaceID: "wsp_test"}); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if _, err = s.Block(task.ID, "external dependency"); err != nil {
				t.Fatal(err)
			}
		}
		if i == 13 {
			if _, err = s.Cancel(task.ID, "done"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Archive(task.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = os.WriteFile(filepath.Join(s.Root(), "tsk_1234567890abcdef.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := s.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxTaskIndexBytes || index.ReturnedCount > 8 || !index.Truncated || index.ActiveCount != 10 || index.BlockedCount != 3 || len(index.Warnings) != 1 {
		t.Fatalf("index %d bytes: %+v", len(data), index)
	}
	for _, item := range index.Items {
		if item.ActiveThread == nil || item.ActiveThread.ID != "main" || item.WorkspaceID != "wsp_test" {
			t.Fatalf("recovery lost: %+v", item)
		}
	}
}

func TestEmptyTaskIndexContainsArray(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index, err := s.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(index)
	if !strings.Contains(string(data), `"items":[]`) || index.Truncated || index.ActiveCount != 0 {
		t.Fatal(string(data))
	}
}
