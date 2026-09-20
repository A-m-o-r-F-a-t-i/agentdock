package taskstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

// Management sidecars survive older versions rewriting their schema-1 task
// records. They contain metadata only and never hold a workspace deletion path.
type taskManagement struct {
	activity.Management
	Title          string    `json:"title,omitempty"`
	ArchiveManaged bool      `json:"archive_managed,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type taskManagementState struct {
	SchemaVersion int                       `json:"schema_version"`
	Items         map[string]taskManagement `json:"items"`
}

func (s *Store) loadManagementLocked() error {
	state := taskManagementState{SchemaVersion: 1, Items: map[string]taskManagement{}}
	path := filepath.Join(s.root, "management-v1.json")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("task management must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	data, err := readTaskStateFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("read task management: %w", err)
		}
	}
	if state.SchemaVersion != 1 || state.Items == nil || len(state.Items) > 20000 {
		return errors.New("invalid task management schema or capacity")
	}
	for id := range state.Items {
		if err = validateID(id); err != nil {
			return err
		}
	}
	s.management = state.Items
	return nil
}
func (s *Store) saveManagementLocked() error {
	state := taskManagementState{SchemaVersion: 1, Items: s.management}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > maxTaskStateFileBytes {
		return errors.New("task management exceeds size limit")
	}
	return atomicfile.Write(filepath.Join(s.root, "management-v1.json"), append(data, '\n'), 0600)
}
func (s *Store) applyManagementLocked(task Task) Task {
	metadata, exists := s.management[task.ID]
	if !exists {
		return task
	}
	task.Pinned, task.Tags = metadata.Pinned, append([]string(nil), metadata.Tags...)
	task.TrashedAt, task.PurgeAfter = metadata.TrashedAt, metadata.PurgeAfter
	if metadata.Title != "" {
		task.Title = metadata.Title
	}
	if metadata.ArchiveManaged {
		task.ArchivedAt = metadata.ArchivedAt
	}
	if metadata.UpdatedAt.After(task.UpdatedAt) {
		task.UpdatedAt = metadata.UpdatedAt
	}
	return task
}
func (s *Store) ManageMetadata(id string, change activity.MetadataChange) (Task, error) {
	release, err := s.acquireStoreLock()
	if err != nil {
		return Task{}, err
	}
	defer release()
	task, err := s.loadLocked(id)
	if err != nil {
		return Task{}, err
	}
	metadata, exists := s.management[id]
	if !exists {
		metadata.Management = activity.Management{Pinned: task.Pinned, Tags: task.Tags, ArchivedAt: task.ArchivedAt, TrashedAt: task.TrashedAt, PurgeAfter: task.PurgeAfter}
	}
	title := task.Title
	if err = activity.ApplyManagement(&metadata.Management, &title, change, time.Now().UTC()); err != nil {
		return Task{}, err
	}
	if change.Action == "rename" {
		metadata.Title = title
	}
	if change.Action == "archive" || change.Action == "unarchive" {
		metadata.ArchiveManaged = true
	}
	metadata.UpdatedAt = time.Now().UTC()
	s.management[id] = metadata
	if err = s.saveManagementLocked(); err != nil {
		return Task{}, err
	}
	return s.applyManagementLocked(task), nil
}

type TaskQuery struct {
	View        string
	Status      string
	WorkspaceID string
	Tag         string
	Search      string
	Offset      int
	Limit       int
	Selection   bool
}
type ManagedTask struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Status         Status    `json:"status"`
	Outcome        string    `json:"outcome,omitempty"`
	Project        string    `json:"project,omitempty"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	CurrentStepID  string    `json:"current_step_id,omitempty"`
	StepCount      int       `json:"step_count"`
	CompletedSteps int       `json:"completed_steps"`
	Blocker        string    `json:"blocker,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	activity.Management
}
type ManagedTaskPage struct {
	Tasks       []ManagedTask `json:"tasks"`
	Total       int           `json:"total"`
	HasMore     bool          `json:"has_more"`
	NextOffset  int           `json:"next_offset"`
	SelectedIDs []string      `json:"selected_ids,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
}

func (s *Store) ManagedTasks(ctx context.Context, query TaskQuery) (ManagedTaskPage, error) {
	page := ManagedTaskPage{Tasks: []ManagedTask{}}
	if query.Offset < 0 || query.Offset > 20000 || len(query.Search) > 512 || len(query.Tag) > 128 {
		return page, errors.New("invalid task query")
	}
	if query.View != "" && query.View != "active" && query.View != "archived" && query.View != "trash" && query.View != "all" {
		return page, errors.New("invalid task view")
	}
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 100
	}
	release, err := s.acquireStoreLock()
	if err != nil {
		return page, err
	}
	defer release()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return page, err
	}
	candidates := []Task{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return page, err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "tsk_") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := readTaskStateFile(filepath.Join(s.root, entry.Name()))
		if readErr != nil {
			if len(page.Warnings) < 8 {
				page.Warnings = append(page.Warnings, "无法读取任务："+entry.Name())
			}
			continue
		}
		task, readErr := decodeTask(data, entry.Name())
		if readErr != nil {
			if len(page.Warnings) < 8 {
				page.Warnings = append(page.Warnings, "无效任务："+entry.Name())
			}
			continue
		}
		task = s.applyManagementLocked(task)
		switch query.View {
		case "trash":
			if task.TrashedAt == nil {
				continue
			}
		case "archived":
			if task.TrashedAt != nil || task.ArchivedAt == nil {
				continue
			}
		case "all":
		default:
			if task.TrashedAt != nil || task.ArchivedAt != nil {
				continue
			}
		}
		if query.Status != "" && string(task.Status) != query.Status || query.WorkspaceID != "" && task.WorkspaceID != query.WorkspaceID {
			continue
		}
		if query.Tag != "" {
			found := false
			for _, tag := range task.Tags {
				if tag == query.Tag {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if query.Search != "" && !strings.Contains(strings.ToLower(task.Title+" "+task.Goal+" "+task.Project+" "+strings.Join(task.Tags, " ")), strings.ToLower(query.Search)) {
			continue
		}
		candidates = append(candidates, task)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Pinned != candidates[j].Pinned {
			return candidates[i].Pinned
		}
		if candidates[i].UpdatedAt.Equal(candidates[j].UpdatedAt) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt)
	})
	page.Total = len(candidates)
	if query.Selection {
		if len(candidates) > 10000 {
			return page, errors.New("selection exceeds 10000 tasks; narrow the filter")
		}
		page.SelectedIDs = []string{}
		for _, task := range candidates {
			page.SelectedIDs = append(page.SelectedIDs, task.ID)
		}
		return page, nil
	}
	start := min(query.Offset, len(candidates))
	end := min(start+query.Limit, len(candidates))
	page.NextOffset = end
	page.HasMore = end < len(candidates)
	for _, task := range candidates[start:end] {
		attached, attachErr := s.attachThreadLocked(task)
		if attachErr == nil {
			task = attached
		} else if len(page.Warnings) < 8 {
			page.Warnings = append(page.Warnings, "任务分支无法读取："+task.ID)
		}
		item := ManagedTask{ID: task.ID, Title: task.Title, Status: task.Status, Outcome: task.Outcome, Project: task.Project, WorkspaceID: task.WorkspaceID, StepCount: len(task.Steps), Blocker: task.Blocker, Summary: shortIndexText(task.Summary, 768), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, Management: activity.Management{Pinned: task.Pinned, Tags: task.Tags, ArchivedAt: task.ArchivedAt, TrashedAt: task.TrashedAt, PurgeAfter: task.PurgeAfter}}
		for _, step := range task.Steps {
			if step.Status == StepCompleted {
				item.CompletedSteps++
			}
		}
		if task.ActiveThread != nil {
			item.CurrentStepID = task.ActiveThread.CurrentStepID
		}
		page.Tasks = append(page.Tasks, item)
	}
	return page, nil
}

type managementDelete struct {
	TaskID string `json:"task_id"`
}

func (s *Store) deletePathsLocked(id string) ([]string, string, error) {
	if err := validateID(id); err != nil {
		return nil, "", err
	}
	taskPath := filepath.Join(s.root, id+".json")
	if info, err := os.Lstat(taskPath); err == nil && !info.Mode().IsRegular() {
		return nil, "", errors.New("task state is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	dir := filepath.Join(s.root, "threads", id)
	// threadPath validates both ancestors and rejects reparse-point/symlink roots.
	if _, err := s.threadPath(id, MainThreadID); err != nil {
		return nil, "", err
	}
	paths := []string{taskPath}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return paths, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, "", errors.New("unexpected entry in task thread storage; no permanent deletion performed")
		}
		threadID := strings.TrimSuffix(entry.Name(), ".json")
		path, err := s.threadPath(id, threadID)
		if err != nil {
			return nil, "", err
		}
		paths = append(paths, path)
	}
	return paths, dir, nil
}
func (s *Store) applyDeleteLocked(id string) error {
	paths, dir, err := s.deletePathsLocked(id)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if dir != "" {
		if err = os.Remove(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	delete(s.management, id)
	return s.saveManagementLocked()
}
func (s *Store) recoverManagementDeleteLocked() error {
	path := filepath.Join(s.root, ".management-delete.json")
	data, err := readTaskStateFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var tx managementDelete
	if err = json.Unmarshal(data, &tx); err != nil {
		return err
	}
	if err = s.applyDeleteLocked(tx.TaskID); err != nil {
		return err
	}
	return os.Remove(path)
}
func (s *Store) deleteManagedLocked(task Task) error {
	if task.TrashedAt == nil {
		return errors.New("move the task to the recycle bin before permanent deletion")
	}
	if _, _, err := s.deletePathsLocked(task.ID); err != nil {
		return err
	}
	data, _ := json.Marshal(managementDelete{TaskID: task.ID})
	path := filepath.Join(s.root, ".management-delete.json")
	if err := atomicfile.Write(path, data, 0600); err != nil {
		return err
	}
	if err := s.applyDeleteLocked(task.ID); err != nil {
		return err
	}
	return os.Remove(path)
}
