package app

import (
	"context"
	"errors"
	"sort"
	"time"
)

const (
	SidebarRecentWindow  = 72 * time.Hour
	SidebarDefaultLimit  = 5
	SidebarExpandedLimit = 15
	SidebarPageSize      = 200
	SidebarMaxItems      = 20000
)

// Each project has its own bounded window. Every response is one snapshot;
// scrolling requests a longer prefix, so changing activity cannot invalidate
// an offset or hide a project behind a global conversation limit.
type SidebarRequest struct {
	View       string         `json:"view"`
	Search     string         `json:"search"`
	Limits     map[string]int `json:"limits,omitempty"`
	SelectedID string         `json:"selected_id,omitempty"`
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
}

type SidebarPage struct {
	ServerNow time.Time         `json:"server_now"`
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

func (r *Runtime) RuntimeConversationSidebar(ctx context.Context, request SidebarRequest) (SidebarPage, error) {
	result := SidebarPage{ServerNow: time.Now().UTC(), Groups: []SidebarGroup{}}
	if len(request.Limits) > SidebarMaxItems {
		return result, errors.New("too many sidebar project windows")
	}
	for _, count := range request.Limits {
		if count < 0 || count != 0 && count != SidebarExpandedLimit && count%SidebarPageSize != 0 {
			return result, errors.New("sidebar limits must be 0, 15 or a positive multiple of 200")
		}
	}
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
	for _, item := range page.Conversations {
		id := conversationWorkspace(item)
		grouped[id] = append(grouped[id], item)
		if item.ID != "" && item.ID == request.SelectedID {
			copy := item
			result.Selected = &copy
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
		projectSidebarRows(&group, items, request.Limits[id], request.Search, request.View, result.ServerNow)
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
	recentOnly := limit == 0 && search == "" && view != "archived" && view != "trash"
	if limit == 0 {
		if recentOnly {
			limit = SidebarDefaultLimit
		} else {
			limit = SidebarPageSize
		}
	}
	for _, item := range items {
		if item.LastActivityAt.After(group.LastActivityAt) {
			group.LastActivityAt = item.LastActivityAt
		}
		recent := !item.LastActivityAt.IsZero() && !item.LastActivityAt.Before(now.Add(-SidebarRecentWindow)) && !item.LastActivityAt.After(now)
		if recent {
			group.RecentCount++
		}
		if len(group.Conversations) < limit && (!recentOnly || recent) {
			group.Conversations = append(group.Conversations, item)
		}
	}
	group.Shown = len(group.Conversations)
	group.HasMore = group.Shown < group.Total
}
