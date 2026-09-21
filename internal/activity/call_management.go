package activity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

// The overlay is outside rotating event segments. Rebuilding the projection or
// replaying a late event cannot restore deleted navigation records.
type CallManagement struct {
	Management
	IsolatedAt *time.Time `json:"isolated_at,omitempty"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}
type callManagementState struct {
	SchemaVersion int                       `json:"schema_version"`
	Items         map[string]CallManagement `json:"items"`
}

func (s *Store) callManagementLocked() (callManagementState, error) {
	state := callManagementState{SchemaVersion: 1, Items: map[string]CallManagement{}}
	path := filepath.Join(s.root, "call-management.json")
	if err := regularPath(path, false); err != nil {
		return state, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return state, err
	}
	if len(data) > 16<<20 {
		return state, errors.New("call management exceeds 16 MiB")
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.SchemaVersion != 1 || state.Items == nil {
		return state, errors.New("invalid call management schema")
	}
	for id := range state.Items {
		if !identifier.MatchString(id) {
			return state, errors.New("invalid management handle")
		}
	}
	return state, nil
}
func callManagementMatches(item CallManagement, view string) bool {
	if item.DeletedAt != nil {
		return false
	}
	switch view {
	case "all":
		return true
	case "trash":
		return item.TrashedAt != nil
	case "archived":
		return item.TrashedAt == nil && item.ArchivedAt != nil
	case "isolated":
		return item.TrashedAt == nil && item.IsolatedAt != nil
	default:
		return item.TrashedAt == nil && item.ArchivedAt == nil && item.IsolatedAt == nil
	}
}
func (s *Store) applyCallManagementLocked(projection *callProjection) error {
	state, err := s.callManagementLocked()
	if err != nil {
		return err
	}
	for id, call := range projection.calls {
		call.CallManagement = state.Items[id]
		// Descendants inherit a parent's hidden state, including late-arriving
		// legacy children which were not present when metadata was changed.
		parent := call.ParentCallID
		for depth := 0; parent != "" && depth < 64; depth++ {
			ancestor := state.Items[parent]
			if ancestor.DeletedAt != nil {
				call.DeletedAt = ancestor.DeletedAt
			}
			if ancestor.TrashedAt != nil {
				call.TrashedAt = ancestor.TrashedAt
				call.PurgeAfter = ancestor.PurgeAfter
			}
			if ancestor.ArchivedAt != nil {
				call.ArchivedAt = ancestor.ArchivedAt
			}
			if ancestor.IsolatedAt != nil {
				call.IsolatedAt = ancestor.IsolatedAt
			}
			value := projection.calls[parent]
			if value == nil {
				break
			}
			parent = value.ParentCallID
		}
	}
	return nil
}

// ManageCall targets a stable call_id (also used by legacy projections). It
// changes only management metadata, never project files or execution status.
func (s *Store) ManageCall(ctx context.Context, id string, change MetadataChange) error {
	if !identifier.MatchString(id) {
		return errors.New("invalid call management handle")
	}
	release, err := s.lock(ctx)
	if err != nil {
		return err
	}
	projection, err := s.projectionLocked(ctx)
	if err != nil {
		release()
		return err
	}
	root := projection.calls[id]
	if root == nil || root.DeletedAt != nil {
		release()
		return ErrCallNotFound
	}
	if change.Action == "delete" && root.TrashedAt == nil {
		release()
		return errors.New("move the record to trash before permanent deletion")
	}
	children := map[string][]string{}
	for childID, call := range projection.calls {
		children[call.ParentCallID] = append(children[call.ParentCallID], childID)
	}
	ids, seen := []string{id}, map[string]bool{id: true}
	for i := 0; i < len(ids); i++ {
		call := projection.calls[ids[i]]
		if !CallTerminal(call.Status) {
			release()
			return errors.New("record or child is still running or awaiting approval")
		}
		for _, child := range children[ids[i]] {
			if !seen[child] {
				seen[child] = true
				ids = append(ids, child)
			}
		}
	}
	state, err := s.callManagementLocked()
	if err != nil {
		release()
		return err
	}
	now := time.Now().UTC()
	for _, member := range ids {
		item := state.Items[member]
		switch change.Action {
		case "isolate":
			item.IsolatedAt = &now
		case "unisolate":
			item.IsolatedAt = nil
		case "delete":
			item.DeletedAt = &now
		case "archive", "unarchive", "trash", "restore":
			title := ""
			if err = ApplyManagement(&item.Management, &title, change, now); err != nil {
				release()
				return err
			}
		default:
			release()
			return errors.New("unsupported call management action")
		}
		state.Items[member] = item
	}
	data, err := json.Marshal(state)
	if err == nil && len(data) > 16<<20 {
		err = errors.New("call management capacity reached")
	}
	if err == nil {
		err = atomicfile.Write(filepath.Join(s.root, "call-management.json"), data, 0600)
	}
	binding, tool := root.Binding, root.ToolName
	release()
	if err != nil {
		return err
	}
	// Advance SSE cursor after the durable overlay so existing windows remove
	// the old row. Event replay does not overwrite the overlay's metadata.
	_, err = s.Append(ctx, Event{Binding: binding, Kind: "call.managed", ToolName: tool, Summary: "管理状态：" + change.Action})
	return err
}
