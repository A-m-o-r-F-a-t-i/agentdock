package taskstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

type threadTransaction struct {
	Task    Task         `json:"task"`
	Threads []TaskThread `json:"threads"`
}

func (s *Store) threadPath(taskID, threadID string) (string, error) {
	if err := validateID(taskID); err != nil {
		return "", err
	}
	if err := validateThreadID(threadID); err != nil {
		return "", err
	}
	for _, dir := range []string{filepath.Join(s.root, "threads"), filepath.Join(s.root, "threads", taskID)} {
		if info, err := os.Lstat(dir); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", errors.New("thread state directory must not be a symlink")
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	path := filepath.Join(s.root, "threads", taskID, threadID+".json")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return "", errors.New("thread state must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func (s *Store) loadThreadLocked(task Task, id string) (TaskThread, error) {
	path, err := s.threadPath(task.ID, id)
	if err != nil {
		return TaskThread{}, err
	}
	data, err := readTaskStateFile(path)
	if os.IsNotExist(err) {
		if id == MainThreadID {
			return virtualMain(task), nil
		}
		return TaskThread{}, fmt.Errorf("%w: %s", ErrThreadNotFound, id)
	}
	if err != nil {
		return TaskThread{}, err
	}
	var thread TaskThread
	if err = json.Unmarshal(data, &thread); err != nil {
		return thread, fmt.Errorf("decode thread: %w", err)
	}
	if err = validateStoredThread(task.ID, thread); err != nil {
		return TaskThread{}, err
	}
	return thread, nil
}

func validateStoredThread(taskID string, thread TaskThread) error {
	if err := validateThreadID(thread.ID); err != nil {
		return err
	}
	if thread.TaskID != taskID {
		return errors.New("thread task ownership mismatch")
	}
	if thread.Status != "open" && thread.Status != "blocked" && thread.Status != "closed" {
		return errors.New("invalid thread status")
	}
	if len(thread.Steps) > 12 {
		return errors.New("thread has too many steps")
	}
	return threadInputValid(ThreadInput{Title: thread.Title, Summary: thread.Summary, NextAction: thread.NextAction, WorkspaceID: thread.WorkspaceID, SourceRef: thread.SourceRef, CheckpointEventID: thread.CheckpointEventID})
}

func (s *Store) listThreadsLocked(task Task) ([]TaskThread, error) {
	main, err := s.loadThreadLocked(task, MainThreadID)
	if err != nil {
		return nil, err
	}
	threads := []TaskThread{main}
	entries, err := os.ReadDir(filepath.Join(s.root, "threads", task.ID))
	if os.IsNotExist(err) {
		return threads, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == MainThreadID+".json" || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		thread, err := s.loadThreadLocked(task, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		threads = append(threads, thread)
		if len(threads) > maxTaskThreads {
			return nil, errors.New("thread inventory exceeds limit")
		}
	}
	sort.SliceStable(threads, func(i, j int) bool { return threads[i].CreatedAt.Before(threads[j].CreatedAt) })
	return threads, nil
}

func (s *Store) attachThreadLocked(task Task) (Task, error) {
	if task.ActiveThreadID == "" {
		task.ActiveThreadID = MainThreadID
	}
	thread, err := s.loadThreadLocked(task, task.ActiveThreadID)
	if err != nil {
		return Task{}, err
	}
	projectThread(&task, thread)
	return task, nil
}

// Legacy task operations project their active branch into separate thread storage.
func (s *Store) saveLocked(task Task) error {
	thread := virtualMain(task)
	if task.ActiveThread != nil {
		thread = *task.ActiveThread
	}
	if task.ActiveThreadID != "" {
		thread.ID = task.ActiveThreadID
	} else {
		task.ActiveThreadID = MainThreadID
	}
	thread.TaskID, thread.Summary = task.ID, task.Summary
	thread.Steps = append([]TaskStep(nil), task.Steps...)
	thread.CurrentStepID, thread.UpdatedAt = currentThreadStep(thread.Steps), task.UpdatedAt
	if thread.WorkspaceID == "" {
		thread.WorkspaceID = task.WorkspaceID
	}
	if task.Status == StatusCompleted {
		thread.Status, thread.ClosedAt = "closed", task.CompletedAt
	} else if task.Status == StatusBlocked {
		thread.Status, thread.BlockReason = "blocked", task.Blocker
	} else if thread.Status == "blocked" && task.Blocker == "" {
		thread.Status, thread.BlockReason = "open", ""
	}
	return s.saveThreadBundleLocked(task, []TaskThread{thread})
}

// A small write-ahead transaction makes Task and Thread replacement recoverable together.
func (s *Store) saveThreadBundleLocked(task Task, threads []TaskThread) error {
	if err := validateID(task.ID); err != nil {
		return err
	}
	if len(threads) > maxTaskThreads {
		return errors.New("too many thread writes")
	}
	for _, thread := range threads {
		if err := validateStoredThread(task.ID, thread); err != nil {
			return err
		}
	}
	task.ActiveThread = nil
	transaction := threadTransaction{Task: task, Threads: threads}
	data, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	if len(data) > maxTaskStateFileBytes {
		return errors.New("thread transaction exceeds size limit")
	}
	path := filepath.Join(s.root, ".thread-transaction.json")
	if err = atomicfile.Write(path, data, 0600); err != nil {
		return err
	}
	if err = s.applyThreadTransactionLocked(transaction); err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *Store) applyThreadTransactionLocked(tx threadTransaction) error {
	if err := validateID(tx.Task.ID); err != nil {
		return err
	}
	if tx.Task.SchemaVersion != SchemaVersion {
		return errors.New("unsupported transaction task schema")
	}
	// Validate the entire transaction before replacing any file.
	for _, thread := range tx.Threads {
		if err := validateStoredThread(tx.Task.ID, thread); err != nil {
			return err
		}
		if _, err := s.threadPath(tx.Task.ID, thread.ID); err != nil {
			return err
		}
	}
	for _, thread := range tx.Threads {
		path, err := s.threadPath(tx.Task.ID, thread.ID)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(thread, "", "  ")
		if err != nil {
			return err
		}
		if err = atomicfile.Write(path, append(data, '\n'), 0600); err != nil {
			return err
		}
	}
	return s.saveTaskOnlyLocked(tx.Task)
}

func (s *Store) recoverThreadTransactionLocked() error {
	path := filepath.Join(s.root, ".thread-transaction.json")
	data, err := readTaskStateFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var tx threadTransaction
	if err = json.Unmarshal(data, &tx); err != nil {
		return fmt.Errorf("recover task/thread transaction: %w", err)
	}
	if len(tx.Threads) > maxTaskThreads {
		return errors.New("invalid thread transaction size")
	}
	if err = s.applyThreadTransactionLocked(tx); err != nil {
		return err
	}
	return os.Remove(path)
}
