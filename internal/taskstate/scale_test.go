package taskstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedTaskFirstPage1000(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 20, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 1000; i++ {
		task := Task{SchemaVersion: 1, ID: fmt.Sprintf("tsk_%08x", i+1), Title: fmt.Sprintf("性能测试任务 %d", i+1), Goal: "1000任务的后端分页", Status: StatusActive, CreatedAt: base, UpdatedAt: base.Add(time.Duration(i) * time.Second)}
		data, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, task.ID+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	page, err := store.ManagedTasks(context.Background(), TaskQuery{View: "active", Limit: 100})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 100 || page.Total != 1000 || !page.HasMore {
		t.Fatalf("task paging failed: %+v", page)
	}
	t.Logf("scale: task_metadata=1000 first_page=100 elapsed_ms=%.3f", float64(elapsed.Microseconds())/1000)
	if elapsed > 2*time.Second {
		t.Errorf("task first-page query exceeded 2s target: %s", elapsed)
	}
}
