package taskstate

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const MainThreadID = "main"
const maxTaskThreads = 64

var ErrThreadNotFound = errors.New("task thread not found")

type TaskThread struct {
	ID                string     `json:"id"`
	TaskID            string     `json:"task_id"`
	Title             string     `json:"title"`
	Goal              string     `json:"goal"`
	Status            string     `json:"status"`
	WorkspaceID       string     `json:"workspace_id,omitempty"`
	CurrentStepID     string     `json:"current_step_id,omitempty"`
	Steps             []TaskStep `json:"steps,omitempty"`
	Summary           string     `json:"summary,omitempty"`
	NextAction        string     `json:"next_action,omitempty"`
	BlockReason       string     `json:"block_reason,omitempty"`
	SourceRef         string     `json:"source_ref,omitempty"`
	ParentThreadID    string     `json:"parent_thread_id,omitempty"`
	CheckpointEventID string     `json:"checkpoint_event_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
}

type ThreadInput struct {
	Title             string
	WorkspaceID       string
	CurrentStepID     string
	CompletedStepIDs  []string
	Summary           string
	NextAction        string
	SourceRef         string
	CheckpointEventID string
	Steps             []TaskStepInput
}

func validateThreadID(id string) error {
	if id == MainThreadID {
		return nil
	}
	if !strings.HasPrefix(id, "thr_") || len(id) != 20 {
		return errors.New("invalid thread id")
	}
	for _, ch := range id[4:] {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return errors.New("invalid thread id")
		}
	}
	return nil
}

func virtualMain(task Task) TaskThread {
	status := "open"
	if task.Status == StatusBlocked {
		status = "blocked"
	}
	if task.Status == StatusCompleted {
		status = "closed"
	}
	thread := TaskThread{ID: MainThreadID, TaskID: task.ID, Title: "main", Goal: task.Goal, Status: status, WorkspaceID: task.WorkspaceID, Steps: append([]TaskStep(nil), task.Steps...), Summary: task.Summary, BlockReason: task.Blocker, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, ClosedAt: task.CompletedAt}
	thread.CurrentStepID = currentThreadStep(thread.Steps)
	return thread
}

func currentThreadStep(steps []TaskStep) string {
	for _, step := range steps {
		if step.Status == StepInProgress {
			return step.ID
		}
	}
	for _, step := range steps {
		if step.Status == StepPending {
			return step.ID
		}
	}
	return ""
}

func threadInputValid(input ThreadInput) error {
	for label, value := range map[string]string{"title": input.Title, "summary": input.Summary, "next_action": input.NextAction, "source_ref": input.SourceRef} {
		limit := maxTaskSummaryBytes
		if label == "title" || label == "source_ref" {
			limit = 512
		}
		if err := validateTextLimit("thread "+label, value, limit); err != nil {
			return err
		}
	}
	if input.WorkspaceID != "" && (!validStepID(input.WorkspaceID) || len(input.WorkspaceID) > 80) {
		return errors.New("invalid workspace_id")
	}
	if input.CheckpointEventID != "" && (!validStepID(input.CheckpointEventID) || len(input.CheckpointEventID) > 80) {
		return errors.New("invalid checkpoint_event_id")
	}
	return nil
}

func projectThread(task *Task, thread TaskThread) {
	task.ActiveThreadID = thread.ID
	task.ActiveThread = &thread
	task.Steps = append([]TaskStep(nil), thread.Steps...)
	task.Summary = thread.Summary
	for _, step := range thread.Steps {
		if step.ID == thread.CurrentStepID && (task.FinalReview == nil || task.FinalReview.Status != FinalReviewPass) {
			task.Phase = step.Phase
			break
		}
	}
}

func (s *Store) GetThread(taskID, threadID string) (TaskThread, error) {
	release, err := s.acquireStoreLock()
	if err != nil {
		return TaskThread{}, err
	}
	defer release()
	task, err := s.loadLocked(taskID)
	if err != nil {
		return TaskThread{}, err
	}
	if threadID == "" {
		threadID = task.ActiveThreadID
	}
	return s.loadThreadLocked(task, threadID)
}

func (s *Store) ListThreads(taskID string) ([]TaskThread, error) {
	release, err := s.acquireStoreLock()
	if err != nil {
		return nil, err
	}
	defer release()
	task, err := s.loadLocked(taskID)
	if err != nil {
		return nil, err
	}
	return s.listThreadsLocked(task)
}

func (s *Store) CreateThread(taskID, sourceID string, fork bool, input ThreadInput) (TaskThread, error) {
	if err := threadInputValid(input); err != nil {
		return TaskThread{}, err
	}
	release, err := s.acquireStoreLock()
	if err != nil {
		return TaskThread{}, err
	}
	defer release()
	task, err := s.loadLocked(taskID)
	if err != nil {
		return TaskThread{}, err
	}
	if err = requireMutable(&task); err != nil {
		return TaskThread{}, err
	}
	if err = requireFinalReviewOpen(&task); err != nil {
		return TaskThread{}, err
	}
	threads, err := s.listThreadsLocked(task)
	if err != nil {
		return TaskThread{}, err
	}
	if len(threads) >= maxTaskThreads {
		return TaskThread{}, errors.New("task thread limit reached")
	}
	if sourceID == "" {
		sourceID = task.ActiveThreadID
	}
	source, err := s.loadThreadLocked(task, sourceID)
	if err != nil {
		return TaskThread{}, err
	}
	now := time.Now().UTC()
	id, err := newOpaqueID("thr_")
	if err != nil {
		return TaskThread{}, err
	}
	thread := source
	thread.ID, thread.Status, thread.BlockReason = id, "open", ""
	thread.ClosedAt = nil
	thread.CreatedAt, thread.UpdatedAt = now, now
	thread.Steps = append([]TaskStep(nil), source.Steps...)
	if fork {
		thread.ParentThreadID = source.ID
	} else {
		thread.ParentThreadID, thread.CheckpointEventID, thread.Summary, thread.NextAction = "", "", "", ""
		if len(input.Steps) > 0 {
			thread.Steps, err = normalizeTaskSteps(input.Steps, now)
			if err != nil {
				return TaskThread{}, err
			}
		} else {
			for i := range thread.Steps {
				thread.Steps[i].Status, thread.Steps[i].UpdatedAt = StepPending, now
			}
		}
	}
	thread.Title = strings.TrimSpace(input.Title)
	if thread.Title == "" {
		thread.Title = source.Title + " branch"
	}
	if input.WorkspaceID != "" {
		thread.WorkspaceID = input.WorkspaceID
	}
	if input.SourceRef != "" {
		thread.SourceRef = input.SourceRef
	}
	thread.CurrentStepID = currentThreadStep(thread.Steps)
	task.UpdatedAt = now
	if err = s.saveThreadBundleLocked(task, []TaskThread{source, thread}); err != nil {
		return TaskThread{}, err
	}
	return thread, nil
}

func (s *Store) SwitchThread(taskID, threadID string) (Task, error) {
	release, err := s.acquireStoreLock()
	if err != nil {
		return Task{}, err
	}
	defer release()
	task, err := s.loadLocked(taskID)
	if err != nil {
		return Task{}, err
	}
	if err = requireMutable(&task); err != nil {
		return Task{}, err
	}
	if err = requireFinalReviewOpen(&task); err != nil {
		return Task{}, err
	}
	thread, err := s.loadThreadLocked(task, threadID)
	if err != nil {
		return Task{}, err
	}
	if thread.Status == "closed" {
		return Task{}, errors.New("closed thread cannot be selected for continuation")
	}
	// Selecting a branch never copies another branch's steps into its state.
	projectThread(&task, thread)
	task.UpdatedAt, task.FinalReview = time.Now().UTC(), nil
	if err = s.saveThreadBundleLocked(task, []TaskThread{thread}); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) UpdateThread(taskID, threadID, action string, input ThreadInput) (TaskThread, error) {
	if err := threadInputValid(input); err != nil {
		return TaskThread{}, err
	}
	release, err := s.acquireStoreLock()
	if err != nil {
		return TaskThread{}, err
	}
	defer release()
	task, err := s.loadLocked(taskID)
	if err != nil {
		return TaskThread{}, err
	}
	if err = requireMutable(&task); err != nil {
		return TaskThread{}, err
	}
	if err = requireFinalReviewOpen(&task); err != nil {
		return TaskThread{}, err
	}
	if threadID == "" {
		threadID = task.ActiveThreadID
	}
	thread, err := s.loadThreadLocked(task, threadID)
	if err != nil {
		return TaskThread{}, err
	}
	now := time.Now().UTC()
	switch action {
	case "checkpoint":
		if err = checkpointThread(&thread, input, now); err != nil {
			return TaskThread{}, err
		}
	case "block":
		if thread.Status != "open" || strings.TrimSpace(input.Summary) == "" {
			return TaskThread{}, errors.New("block requires an open thread and a reason")
		}
		thread.Status, thread.BlockReason, thread.Summary = "blocked", input.Summary, input.Summary
	case "resume":
		if thread.Status != "blocked" {
			return TaskThread{}, errors.New("resume requires a blocked thread")
		}
		thread.Status, thread.BlockReason = "open", ""
		if input.Summary != "" {
			thread.Summary = input.Summary
		}
	case "close":
		if thread.Status == "closed" {
			return thread, nil
		}
		thread.Status, thread.ClosedAt = "closed", &now
		if input.Summary != "" {
			thread.Summary = input.Summary
		}
	case "bind":
		if task.Status != StatusActive || thread.Status != "open" {
			return TaskThread{}, errors.New("execution binding requires an active task and open thread")
		}
		if input.CurrentStepID != "" {
			found := false
			for _, step := range thread.Steps {
				if step.ID == input.CurrentStepID {
					found = true
				}
			}
			if !found {
				return TaskThread{}, errors.New("step does not belong to selected thread")
			}
		}
		if input.WorkspaceID != "" {
			thread.WorkspaceID = input.WorkspaceID
		}
	case "checkpoint_ref":
		thread.CheckpointEventID = input.CheckpointEventID
	default:
		return TaskThread{}, errors.New("unsupported thread mutation")
	}
	thread.UpdatedAt, task.UpdatedAt = now, now
	if thread.ID == task.ActiveThreadID {
		projectThread(&task, thread)
		if task.FinalReview != nil && task.FinalReview.Status == FinalReviewFailed {
			task.FinalReview = nil
		}
	}
	if err = s.saveThreadBundleLocked(task, []TaskThread{thread}); err != nil {
		return TaskThread{}, err
	}
	return thread, nil
}

func checkpointThread(thread *TaskThread, input ThreadInput, now time.Time) error {
	if thread.Status != "open" {
		return errors.New("checkpoint requires an open thread")
	}
	if strings.TrimSpace(input.Summary) == "" {
		return errors.New("thread checkpoint summary is required")
	}
	steps := append([]TaskStep(nil), thread.Steps...)
	indices := map[string]int{}
	for i, step := range steps {
		indices[step.ID] = i
	}
	completed := map[string]bool{}
	for _, id := range input.CompletedStepIDs {
		idx, ok := indices[id]
		if !ok {
			return fmt.Errorf("thread step %s not found", id)
		}
		if !validStepTransition(steps[idx].Status, StepCompleted) {
			return errors.New("invalid step transition")
		}
		completed[id] = true
		steps[idx].Status, steps[idx].UpdatedAt = StepCompleted, now
	}
	if input.CurrentStepID != "" {
		idx, ok := indices[input.CurrentStepID]
		if !ok || completed[input.CurrentStepID] {
			return errors.New("invalid current thread step")
		}
		if !validStepTransition(steps[idx].Status, StepInProgress) {
			return errors.New("completed step cannot be restarted")
		}
		for _, step := range steps {
			if step.Status == StepInProgress && step.ID != input.CurrentStepID {
				return errors.New("another thread step is still in progress")
			}
		}
		steps[idx].Status, steps[idx].UpdatedAt = StepInProgress, now
	}
	thread.Steps = steps
	thread.CurrentStepID = currentThreadStep(steps)
	thread.Summary, thread.NextAction = input.Summary, input.NextAction
	if input.WorkspaceID != "" {
		thread.WorkspaceID = input.WorkspaceID
	}
	if input.SourceRef != "" {
		thread.SourceRef = input.SourceRef
	}
	return nil
}

func (s *Store) Cancel(id, reason string) (Task, error) {
	return s.mutate(id, func(task *Task, now time.Time) error {
		if err := requireMutable(task); err != nil {
			return err
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return errors.New("cancellation reason is required")
		}
		if err := validateTextLimit("cancel reason", reason, maxTaskSummaryBytes); err != nil {
			return err
		}
		task.Status, task.Outcome, task.CancelReason = StatusCompleted, "cancelled", reason
		task.CancelledAt, task.CompletedAt, task.Summary = &now, &now, reason
		appendTaskEvent(task, Event{Type: "cancelled", Summary: reason, CreatedAt: now})
		return nil
	})
}

func (s *Store) Archive(id string, archive bool) (Task, error) {
	return s.mutate(id, func(task *Task, now time.Time) error {
		if task.Status != StatusCompleted {
			return errors.New("only completed tasks may be archived")
		}
		if archive {
			task.ArchivedAt = &now
		} else {
			task.ArchivedAt = nil
		}
		return nil
	})
}
