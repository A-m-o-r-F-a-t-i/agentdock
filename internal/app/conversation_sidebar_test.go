package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func sidebarFixture(now time.Time, total, recent int) []ConversationItem {
	items := make([]ConversationItem, total)
	for index := range items {
		at := now.Add(-time.Duration(index+1) * time.Second)
		if index >= recent {
			at = now.Add(-80*time.Hour - time.Duration(index)*time.Minute)
		}
		items[index] = ConversationItem{Conversation: activity.Conversation{ID: fmt.Sprintf("conv_%05d", index)}, LastActivityAt: at}
	}
	return items
}

func TestSidebarActiveDefaultAndFiveTwentySteps(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for _, recent := range []int{0, 2, 5, 10, 20} {
		items := sidebarFixture(now, 70, recent)
		for _, limit := range []int{0, 5, 20, 40, 60, 80, 0} {
			group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
			projectSidebarRows(&group, items, limit, "", "active", now)
			want := recent
			if limit > 0 {
				want = min(limit, len(items))
			}
			if group.Shown != want || group.RecentCount != recent || group.HasMore != (limit > 0 && want < len(items)) {
				t.Fatalf("recent=%d limit=%d group=%+v", recent, limit, group)
			}
		}
		collapsed := SidebarGroup{Mode: "collapsed", Total: len(items)}
		projectSidebarRows(&collapsed, items, 0, "", "active", now)
		if collapsed.Shown != 0 || collapsed.HasMore {
			t.Fatal("collapsed project retained children or ellipsis")
		}
		group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
		projectSidebarRows(&group, items, 0, "search", "active", now)
		if group.Shown != 70 {
			t.Fatal("history search was restricted to visible recent rows")
		}
	}
}

func TestSidebarActivityBoundaryAndRuntimeEvidence(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	items := []ConversationItem{
		{LastActivityAt: now.Add(-119 * time.Second)},
		{LastActivityAt: now.Add(-120 * time.Second)},
		{LastActivityAt: now.Add(-121 * time.Second)},
		{LastActivityAt: now.Add(time.Second)}, {},
		{LastActivityAt: now.Add(-time.Hour), InFlight: true},
		{LastActivityAt: now.Add(-time.Hour), Statistics: activity.CallStats{Running: 1, Pending: 1}},
		{Conversation: activity.Conversation{TerminatedAt: &now}, InFlight: true},
	}
	group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
	projectSidebarRows(&group, items, 0, "", "active", now)
	if group.Shown != 2 || group.RecentCount != 2 || group.HasMore {
		t.Fatalf("boundary=%+v", group)
	}
}

func TestSidebarHistoryIsNotCappedAtTwentyThousand(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	items := sidebarFixture(now, 20201, 0)
	group := SidebarGroup{Total: len(items), Conversations: []ConversationItem{}}
	projectSidebarRows(&group, items, 20220, "", "active", now)
	if group.Shown != len(items) || group.HasMore {
		t.Fatalf("history truncated: shown=%d total=%d", group.Shown, len(items))
	}
}

func TestSidebarHistorySnapshotSurvivesMovingActivity(t *testing.T) {
	now := time.Now().UTC()
	cache := sidebarHistoryCache{}
	initial := sidebarFixture(now, 70, 2)
	ordered, arrivals, token, reset, err := cache.order("project-a\x00active", "", initial, now)
	if err != nil || reset || token == "" || len(ordered) != 70 || len(arrivals) != 0 {
		t.Fatal("initial snapshot failed", err)
	}
	current := []ConversationItem{{Conversation: activity.Conversation{ID: "new-conversation"}, LastActivityAt: now.Add(time.Second)}}
	for index := len(initial) - 1; index >= 0; index-- {
		if index != 11 {
			current = append(current, initial[index])
		}
	}
	for _, limit := range []int{5, 20, 40, 60, 80} {
		ordered, arrivals, again, reset, err := cache.order("project-a\x00active", token, current, now.Add(time.Second))
		if err != nil || again != token || reset || len(ordered) != 69 || len(arrivals) != 1 {
			t.Fatal("snapshot changed", err)
		}
		seen := map[string]bool{}
		for _, row := range append(arrivals, ordered[:min(limit, len(ordered))]...) {
			if seen[row.ID] {
				t.Fatal("duplicate row")
			}
			seen[row.ID] = true
		}
		if ordered[0].ID != initial[0].ID || ordered[11].ID != initial[12].ID {
			t.Fatal("live ordering moved the frozen history boundary")
		}
	}
	if _, _, _, _, err := cache.order("project-b\x00active", token, current, now); err == nil {
		t.Fatal("cross-project cursor accepted")
	}
	_, _, fresh, reset, err := cache.order("project-a\x00active", token, current, now.Add(sidebarHistoryTTL+2*time.Second))
	if err != nil || !reset || fresh == token {
		t.Fatal("expired cursor was not explicitly reset")
	}
}

func TestSidebarHistoryCacheHasBoundedLifetimeAndCapacity(t *testing.T) {
	cache := sidebarHistoryCache{}
	now := time.Now().UTC()
	for index := 0; index < sidebarHistoryCapacity+10; index++ {
		if _, _, _, _, err := cache.order(fmt.Sprint(index), "", sidebarFixture(now, 10, 0), now.Add(time.Duration(index)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != sidebarHistoryCapacity || cache.ids != sidebarHistoryCapacity*10 {
		t.Fatal("unbounded sidebar cache", len(cache.entries), cache.ids)
	}
	if _, _, _, _, err := cache.order("after-expiry", "", nil, now.Add(sidebarHistoryTTL+time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(cache.entries) != 1 || cache.ids != 0 {
		t.Fatal("expired IDs were retained")
	}
}
