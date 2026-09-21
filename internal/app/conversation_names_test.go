package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func Test113LegacyConversationListNamesInOneBatch(t *testing.T) {
	r := executionTestRuntime(t)
	taskID := scopeTask(t, r, scopeHost("names-template"), "共享任务名称")
	items := map[string]activity.Conversation{}
	now := time.Now().UTC()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("conv_%032x", i+1)
		item := activity.Conversation{ID: id, Title: "新对话", Source: "mcp:http", Attribution: "host_metadata", CreatedAt: now, UpdatedAt: now, TaskIDs: []string{taskID}}
		if i == 0 {
			item.Title, item.TaskIDs = "旧版手动名称", nil
		}
		if i == 1 {
			item.Title, item.TitleSource = "宿主名称", "host"
		}
		if i == 2 {
			item.TaskIDs = nil
		}
		items[id] = item
	}
	data, err := json.Marshal(map[string]any{"schema_version": 1, "items": items})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.cfg.AgentDockHome, "execution", "conversations.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	page, err := r.RuntimeConversations(ctx, ExecutionListQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1000 || len(page.Conversations) != 200 || !page.HasMore {
		t.Fatalf("Incorrect page: total=%d rows=%d", page.Total, len(page.Conversations))
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("Batch naming exceeded deadline: %v", elapsed)
	}
	persisted, err := r.conversations.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range persisted {
		if item.Title == "" || item.Title == "新对话" {
			t.Fatalf("Placeholder remained: %s", item.ID)
		}
		if item.ID == fmt.Sprintf("conv_%032x", 1) && item.Title != "旧版手动名称" {
			t.Fatal("Old manual name changed")
		}
		if item.ID == fmt.Sprintf("conv_%032x", 2) && item.Title != "宿主名称" {
			t.Fatal("Host name changed")
		}
	}
	search, err := r.RuntimeConversations(ctx, ExecutionListQuery{Search: "共享任务名称", Selection: true})
	if err != nil || search.Total != 997 || len(search.SelectedIDs) != 997 {
		t.Fatalf("Name search or selection lost results: total=%d err=%v", search.Total, err)
	}
	second, err := r.RuntimeConversations(ctx, ExecutionListQuery{Offset: page.NextOffset, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range page.Conversations {
		seen[item.ID] = true
	}
	for _, item := range second.Conversations {
		if seen[item.ID] {
			t.Fatal("Pagination duplicated an ID")
		}
	}
}

func Test113ConversationListHonorsCancellation(t *testing.T) {
	r := executionTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.RuntimeConversations(ctx, ExecutionListQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancelled query returned %v", err)
	}
}
