package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

type capabilityObservation struct {
	seen    map[string]string
	updated time.Time
}
type capabilityUpdates struct {
	mu            sync.Mutex
	conversations map[string]*capabilityObservation
}

func (r *Runtime) rememberCapabilityAccess(binding activity.Binding, tool string, args map[string]any, result Result) {
	if binding.ConversationID == "" || binding.SourceOwnerKey == "" || binding.ParentCallID != "" {
		return
	}
	names := []string{}
	switch tool {
	case "mcp_tool_call", "mcp_tool_inspect":
		if name, _, ok := strings.Cut(stringArg(args, "name"), ":"); ok {
			names = append(names, name)
		}
	case "mcp_tool_search":
		if server := stringArg(args, "server"); server != "" {
			names = append(names, server)
		}
	case "plugin_load", "agentdock_context":
		field := "mcp_servers"
		if tool == "agentdock_context" {
			field = "dynamic_mcp"
		}
		var entries []struct {
			Name string `json:"name"`
		}
		if raw, err := json.Marshal(result[field]); err == nil {
			_ = json.Unmarshal(raw, &entries)
		}
		for _, entry := range entries {
			names = append(names, entry.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	snapshots := r.capabilityManager.Snapshots(names)
	r.capabilityUpdates.mu.Lock()
	defer r.capabilityUpdates.mu.Unlock()
	if r.capabilityUpdates.conversations == nil {
		r.capabilityUpdates.conversations = map[string]*capabilityObservation{}
	}
	key := binding.SourceOwnerKey + "/" + binding.ConversationID
	entry := r.capabilityUpdates.conversations[key]
	if entry == nil {
		if len(r.capabilityUpdates.conversations) >= 1024 {
			var oldest string
			var at time.Time
			for id, value := range r.capabilityUpdates.conversations {
				if oldest == "" || value.updated.Before(at) {
					oldest, at = id, value.updated
				}
			}
			delete(r.capabilityUpdates.conversations, oldest)
		}
		entry = &capabilityObservation{seen: map[string]string{}}
		r.capabilityUpdates.conversations[key] = entry
	}
	for _, snapshot := range snapshots {
		if _, exists := entry.seen[snapshot.Name]; !exists && len(entry.seen) >= 64 {
			continue
		}
		// Search/inspect/load/context already returned this catalog. A tool execution
		// consumes the previously seen catalog, so it must still surface later changes.
		if _, exists := entry.seen[snapshot.Name]; !exists || tool != "mcp_tool_call" {
			entry.seen[snapshot.Name] = snapshot.Revision
		}
	}
	entry.updated = time.Now()
}

func (r *Runtime) capabilityNoticeBlocks(binding activity.Binding) []string {
	if r.capabilityManager == nil || binding.ConversationID == "" {
		return nil
	}
	key := binding.SourceOwnerKey + "/" + binding.ConversationID
	r.capabilityUpdates.mu.Lock()
	defer r.capabilityUpdates.mu.Unlock()
	entry := r.capabilityUpdates.conversations[key]
	if entry == nil {
		return nil
	}
	names := make([]string, 0, len(entry.seen))
	for name := range entry.seen {
		names = append(names, name)
	}
	sort.Strings(names)
	summaries := r.capabilityManager.Snapshots(names)
	notices := []mcpclient.ServerSummary{}
	for _, snapshot := range summaries {
		if snapshot.Revision == entry.seen[snapshot.Name] {
			continue
		}
		notices = append(notices, snapshot)
		entry.seen[snapshot.Name] = snapshot.Revision
		if len(notices) == 4 {
			break
		}
	}
	if len(notices) == 0 {
		return nil
	}
	// No executable, URL, environment, credential or third-party output is copied.
	type notice struct {
		Name        string `json:"name"`
		Revision    string `json:"revision"`
		Description string `json:"description"`
		Version     string `json:"server_version,omitempty"`
		Count       *int   `json:"tool_count,omitempty"`
	}
	payload := []notice{}
	for _, snapshot := range notices {
		item := notice{Name: snapshot.Name, Revision: snapshot.Revision, Description: truncateString(snapshot.Description, 512), Version: snapshot.ServerVersion}
		if snapshot.ToolCountKnown {
			count := snapshot.ToolCount
			item.Count = &count
		}
		payload = append(payload, item)
	}
	raw, _ := json.Marshal(payload)
	return []string{fmt.Sprintf("[[AGENTDOCK_CAPABILITY_UPDATE_V1]]\n%s\nThe listed MCP capabilities changed. Inspect the relevant tool before the next use; do not load unrelated Heavy plugins.\n[[END_AGENTDOCK_CAPABILITY_UPDATE_V1]]", raw)}
}
