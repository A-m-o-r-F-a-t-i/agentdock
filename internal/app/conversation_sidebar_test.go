package app

import (
	"fmt"
	"github.com/uvwt/agentdock/internal/activity"
	"testing"
	"time"
)

func TestSidebarRecentFiveFifteenAllAndReset(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, recent := range []int{0, 2, 5, 10, 20} {
		items := []ConversationItem{}
		for index := 0; index < 50; index++ {
			at := now.Add(-time.Duration(index) * time.Minute)
			if index >= recent {
				at = now.Add(-80*time.Hour - time.Duration(index)*time.Minute)
			}
			items = append(items, ConversationItem{Conversation: activity.Conversation{ID: fmt.Sprintf("conv_%d", index)}, LastActivityAt: at})
		}
		for _, limit := range []int{0, 15, 200, 0} {
			group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
			projectSidebarRows(&group, items, limit, "", "active", now)
			want := min(recent, 5)
			if limit == 15 {
				want = 15
			}
			if limit == 200 {
				want = 50
			}
			if group.Shown != want || group.RecentCount != recent || group.HasMore != (want < 50) {
				t.Fatalf("recent=%d limit=%d group=%+v", recent, limit, group)
			}
		}
		group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
		projectSidebarRows(&group, items, 0, "search", "active", now)
		if group.Shown != 50 {
			t.Fatal("history search was restricted to recent rows")
		}
	}
}
func TestSidebarRecentBoundaryAndUnknownTime(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	items := []ConversationItem{{LastActivityAt: now.Add(-72 * time.Hour)}, {LastActivityAt: now.Add(-72*time.Hour - time.Nanosecond)}, {LastActivityAt: now.Add(time.Second)}, {}}
	group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
	projectSidebarRows(&group, items, 0, "", "active", now)
	if group.Shown != 1 || group.RecentCount != 1 || !group.HasMore {
		t.Fatalf("boundary=%+v", group)
	}
}

func TestSidebarAllViewIsNotCappedAtTwentyThousand(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	items := make([]ConversationItem, 20201)
	for index := range items {
		items[index].LastActivityAt = now
	}
	group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
	projectSidebarRows(&group, items, 20400, "", "active", now)
	if group.Shown != len(items) || group.HasMore {
		t.Fatalf("all view truncated: shown=%d total=%d", group.Shown, len(items))
	}
}
