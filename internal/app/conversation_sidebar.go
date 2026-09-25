package app

import (
	"context"
	"errors"
	"sort"
	"time"
)

const (
	SidebarRecentWindow  = 120 * time.Second
	SidebarDefaultLimit  = 5
	SidebarExpandedLimit = 20
	SidebarPageSize      = 20
	SidebarMaxItems      = 20000 // Maximum number of project selectors, not history rows.
)

type SidebarRequest struct {
	View        string            `json:"view"`
	Search      string            `json:"search"`
	Limits      map[string]int    `json:"limits,omitempty"`
	Modes       map[string]string `json:"modes,omitempty"`
	Cursors     map[string]string `json:"cursors,omitempty"`
	DefaultMode string            `json:"default_mode,omitempty"`
	SelectedID  string            `json:"selected_id,omitempty"`
}

type SidebarGroup struct {
	ID             string             `json:"workspace_id"`
	Title          string             `json:"title"`
	Root           string             `json:"root,omitempty"`
	Total          int                `json:"total"`
	RecentCount    int                `json:"recent_count"`
	LastActivityAt time.Time          `json:"last_activity_at"`
	Conversations  []ConversationItem `json:"conversations"`
	HasMore        bool               `json:"has_more"`
	Shown          int                `json:"shown"`
	Mode           string             `json:"mode"`
	HistoryCursor  string             `json:"history_cursor,omitempty"`
	HistoryReset   bool               `json:"history_reset,omitempty"`
	HistoryLimit   int                `json:"history_limit,omitempty"`
}

type SidebarPage struct {
	ServerNow time.Time         `json:"server_now"`
	LatestSeq uint64            `json:"latest_seq"`
	Groups    []SidebarGroup    `json:"groups"`
	Selected  *ConversationItem `json:"selected,omitempty"`
	Total     int               `json:"total"`
}

func conversationWorkspace(item ConversationItem) string {
	if item.IsUnattributed {
		return "unattributed"
	}
	if item.State.WorkspaceID != "" {
		return item.State.WorkspaceID
	}
	if len(item.WorkspaceIDs) > 0 {
		return item.WorkspaceIDs[0]
	}
	return "unassigned"
}

func sidebarActive(item ConversationItem, now time.Time) bool {
	if item.IsUnattributed || item.TerminatedAt != nil || item.TrashedAt != nil {
		return false
	}
	if item.InFlight {
		return true
	}
	return !item.LastActivityAt.IsZero() && !item.LastActivityAt.After(now) && now.Sub(item.LastActivityAt) < SidebarRecentWindow
}

func (r *Runtime) RuntimeConversationSidebar(ctx context.Context, request SidebarRequest) (SidebarPage, error) {
	result := SidebarPage{ServerNow: time.Now().UTC(), Groups: []SidebarGroup{}}
	if len(request.Limits) > SidebarMaxItems || len(request.Modes) > SidebarMaxItems || len(request.Cursors) > SidebarMaxItems {
		return result, errors.New("too many sidebar project selectors")
	}
	for _, count := range request.Limits {
		if count < 0 || count != 0 && count != SidebarDefaultLimit && count%SidebarPageSize != 0 {
			return result, errors.New("sidebar limits must be 0, 5, or a positive multiple of 20")
		}
	}
	if request.DefaultMode != "" && request.DefaultMode != "auto" && request.DefaultMode != "collapsed" {
		return result, errors.New("invalid default sidebar mode")
	}
	for _, mode := range request.Modes {
		if mode != "auto" && mode != "collapsed" && mode != "history" {
			return result, errors.New("invalid project sidebar mode")
		}
	}
	for _, cursor := range request.Cursors {
		if len(cursor) > 64 {
			return result, errors.New("invalid sidebar history cursor")
		}
	}
	if request.SelectedID == "unattributed" {
		return result, errors.New("SIDEBAR_RESERVED_KEY")
	}
	// Capture the stream boundary first: a call arriving during projection is
	// either in this snapshot or replayed after this cursor, never missed.
	cursor, err := r.activity.CallCursor(ctx)
	if err != nil {
		return result, err
	}
	result.LatestSeq = cursor
	page, err := r.RuntimeConversations(ctx, ExecutionListQuery{View: request.View, Search: request.Search, snapshot: true})
	if err != nil {
		return result, err
	}
	result.ServerNow, result.Total = page.ServerNow, page.Total
	workspaces, _, err := r.workspaceRegistry.List(ctx)
	if err != nil {
		return result, err
	}
	names, roots := map[string]string{}, map[string]string{}
	for _, workspace := range workspaces {
		names[workspace.ID], roots[workspace.ID] = workspace.Name, workspace.Root
	}
	grouped := map[string][]ConversationItem{}
	navigationKeys := map[string]struct{}{}
	for _, item := range page.Conversations {
		key, keyErr := sidebarNavigationKey(item)
		if keyErr != nil {
			return result, keyErr
		}
		if _, duplicate := navigationKeys[key]; duplicate {
			return result, errors.New("SIDEBAR_NAVIGATION_KEY_DUPLICATE")
		}
		navigationKeys[key] = struct{}{}
		id := conversationWorkspace(item)
		if id == "unattributed" && !item.IsUnattributed {
			return result, errors.New("SIDEBAR_UNATTRIBUTED_GROUP_INVALID")
		}
		grouped[id] = append(grouped[id], item)
		if item.ID != "" && item.ID == request.SelectedID {
			copy := item
			result.Selected = &copy
		}
	}
	// A search result is navigation only. Keep the right-hand conversation even
	// when it is no longer among the search or activity rows.
	if result.Selected == nil && request.SelectedID != "" {
		if selected, err := r.conversations.Get(ctx, request.SelectedID); err == nil {
			_, stats, err := r.activity.CallStatistics(ctx)
			if err != nil {
				return result, err
			}
			summary := stats[selected.ID]
			last := time.Time{}
			if summary.LastActivityAt != nil {
				last = *summary.LastActivityAt
			}
			result.Selected = &ConversationItem{Conversation: selected, Statistics: summary, LastActivityAt: last, InFlight: r.confirmedConversationActivity()[selected.ID]}
		}
	}
	for id, items := range grouped {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		title := names[id]
		if title == "" {
			title = "历史工作区"
		}
		if id == "unattributed" {
			title = "未归属记录"
		}
		if id == "unassigned" {
			title = "未关联项目"
		}
		group := SidebarGroup{ID: id, Title: title, Root: roots[id], Total: len(items), Conversations: []ConversationItem{}}
		mode, limit := request.Modes[id], request.Limits[id]
		if mode == "" {
			mode = request.DefaultMode
			if mode == "" {
				mode = "auto"
			}
			if limit > 0 {
				mode = "history"
			}
		}
		if mode == "auto" && (request.Search != "" || request.View == "archived" || request.View == "trash") {
			mode = "history"
			if limit == 0 {
				limit = 200
			}
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].LastActivityAt.Equal(items[j].LastActivityAt) {
				return items[i].ID < items[j].ID
			}
			return items[i].LastActivityAt.After(items[j].LastActivityAt)
		})
		group.Mode = mode
		projectSidebarRows(&group, items, limit, "", "active", result.ServerNow)
		if mode == "collapsed" {
			group.Conversations = []ConversationItem{}
			group.HasMore = false
		}
		if mode == "history" {
			if limit == 0 {
				limit = SidebarDefaultLimit
			}
			ordered, arrivals, token, reset, err := r.sidebarHistory.order(id+"\x00"+request.View+"\x00"+request.Search, request.Cursors[id], items, result.ServerNow)
			if err != nil {
				return result, err
			}
			group.Conversations = append(arrivals, ordered[:min(limit, len(ordered))]...)
			group.HistoryCursor, group.HistoryReset, group.HistoryLimit = token, reset, limit
			group.HasMore = len(ordered) > limit
		}
		group.Shown = len(group.Conversations)
		result.Groups = append(result.Groups, group)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		a, b := result.Groups[i], result.Groups[j]
		if a.LastActivityAt.Equal(b.LastActivityAt) {
			return a.ID < b.ID
		}
		return a.LastActivityAt.After(b.LastActivityAt)
	})
	return result, nil
}

func projectSidebarRows(group *SidebarGroup, items []ConversationItem, limit int, search, view string, now time.Time) {
	history := group.Mode == "history" || group.Mode == "" && (limit > 0 || search != "" || view == "archived" || view == "trash")
	if history && limit == 0 {
		limit = 200
	}
	for _, item := range items {
		if item.LastActivityAt.After(group.LastActivityAt) {
			group.LastActivityAt = item.LastActivityAt
		}
		active := sidebarActive(item, now)
		if active {
			group.RecentCount++
		}
		if group.Mode == "collapsed" {
			continue
		}
		if history && len(group.Conversations) < limit || !history && active {
			group.Conversations = append(group.Conversations, item)
		}
	}
	group.Shown = len(group.Conversations)
	group.HasMore = history && group.Shown < group.Total
}
