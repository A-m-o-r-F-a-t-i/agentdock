package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
)

// rollbackJournal 同时记录文件快照和服务运行态。
// 回滚顺序必须是：停掉新进程 → 还原文件 → 重载 unit → 按安装前状态拉起旧服务。
// 只有 Restore 成功才允许把事务写成 rolled_back；还原失败必须是 failed/rollback_failed。
type rollbackJournal struct {
	Dir            string                `json:"dir"`
	TransactionID  string                `json:"transaction_id,omitempty"`
	Backups        []journalBackup       `json:"backups"`
	Created        []string              `json:"created"`
	Services       []journalService      `json:"services,omitempty"`
	RestoreEntries []journalRestoreEntry `json:"restore_entries,omitempty"`
	RestoreStatus  string                `json:"restore_status,omitempty"`
}

// journalService 记住安装前服务是否在跑，以及本次事务有没有动过它。
type journalService struct {
	Manager       string `json:"manager"`
	Name          string `json:"name"`
	Domain        string `json:"domain,omitempty"`
	Plist         string `json:"plist,omitempty"`
	WasActive     bool   `json:"was_active"`
	WasEnabled    bool   `json:"was_enabled"`
	StopAttempted bool   `json:"stop_attempted,omitempty"`
	StoppedByUs   bool   `json:"stopped_by_us"`
	LoadAttempted bool   `json:"load_attempted,omitempty"`
	LoadedByUs    bool   `json:"loaded_by_us"`
	StartedByUs   bool   `json:"started_by_us"`
}

type journalBackup struct {
	NativeVersion int    `json:"native_metadata_version,omitempty"`
	Original      string `json:"original"`
	Backup        string `json:"backup,omitempty"`
	Existed       bool   `json:"existed"`
	Digest        string `json:"digest,omitempty"`
}

func newJournal(stateRoot, transactionID string) *rollbackJournal {
	return &rollbackJournal{
		Dir:           filepath.Join(stateRoot, "install", "rollback", transactionID),
		TransactionID: transactionID,
	}
}

func loadJournal(stateRoot, transactionID string) (*rollbackJournal, error) {
	dir := filepath.Join(stateRoot, "install", "rollback", transactionID)
	data, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var journal rollbackJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("parse rollback journal: %w", err)
	}
	journal.Dir = dir
	if journal.TransactionID == "" {
		journal.TransactionID = transactionID
	}
	return &journal, nil
}

func discardJournal(stateRoot, transactionID string) {
	transactionID = strings.TrimSpace(transactionID)
	if stateRoot == "" || transactionID == "" {
		return
	}
	// committed / rolled_back 不再依赖 rollback journal。残留 journal 只是垃圾，
	// 删除失败不能把一次已经终态的事务打成失败。
	_ = os.RemoveAll(filepath.Join(stateRoot, "install", "rollback", transactionID))
}

func (journal *rollbackJournal) Snapshot(path string) error {
	if journal == nil {
		return errors.New("rollback journal is required")
	}
	if path == "" {
		return nil
	}
	if journal.RestoreStatus != "" {
		return errors.New("cannot add snapshots after restoration has started")
	}
	path = filepath.Clean(path)
	for _, existing := range journal.Backups {
		if restorePathKey(existing.Original) == restorePathKey(path) {
			return nil
		}
	}
	if err := validateRestorePath(path); err != nil {
		return err
	}
	if restoreWithin(journal.Dir, path) || restoreWithin(path, journal.Dir) {
		return errors.New("snapshot overlaps its journal")
	}
	_, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			next := journal.nextState()
			next.Backups = append(next.Backups, journalBackup{Original: path, Existed: false})
			return journal.commit(next)
		}
		return err
	}
	if err := os.MkdirAll(journal.Dir, 0o700); err != nil {
		return err
	}
	container, err := os.MkdirTemp(journal.Dir, "snapshot-")
	if err != nil {
		return err
	}
	if err := securepath.EnsurePrivate(container); err != nil {
		return err
	}
	backup := filepath.Join(container, "payload")
	digest, err := copyBackupTree(context.Background(), path, backup)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", path, err)
	}
	verified, err := copyBackupTree(context.Background(), backup, "")
	if err != nil || digest != verified {
		return fmt.Errorf("snapshot verification failed: %s: %w", path, errors.Join(err, errors.New("digest mismatch")))
	}
	next := journal.nextState()
	next.Backups = append(next.Backups, journalBackup{Original: path, Backup: backup, Existed: true, Digest: digest, NativeVersion: backupNativeMetadataVersion})
	return journal.commit(next)
}

func (journal *rollbackJournal) NoteCreated(path string) error {
	if path == "" {
		return nil
	}
	next := journal.nextState()
	next.Created = append(next.Created, path)
	return journal.commit(next)
}

func (journal *rollbackJournal) NoteService(service journalService) error {
	if journal == nil {
		return errors.New("rollback journal is required")
	}
	next := journal.nextState()
	next.Services = append(next.Services, service)
	return journal.commit(next)
}

func (journal *rollbackJournal) hasService(name string) bool {
	if journal == nil {
		return false
	}
	for _, service := range journal.Services {
		if service.Name == name {
			return true
		}
	}
	return false
}

func (journal *rollbackJournal) updateService(name string, mutate func(*journalService)) error {
	for i := range journal.Services {
		if journal.Services[i].Name == name {
			next := journal.nextState()
			mutate(&next.Services[i])
			return journal.commit(next)
		}
	}
	return fmt.Errorf("journal missing service %s", name)
}

func (journal *rollbackJournal) Restore(ctx context.Context, request Request) error {
	return journal.restore(ctx, request, true)
}

func (journal *rollbackJournal) restore(ctx context.Context, request Request, resumeServices bool) error {
	if journal == nil {
		return errors.New("rollback journal is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var failures []error
	if journal.RestoreStatus == "complete" {
		if resumeServices {
			for _, service := range journal.Services {
				if err := restoreJournalService(ctx, request, service); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if len(failures) > 0 {
			return errors.Join(failures...)
		}
		return journal.finishRestore()
	}
	if err := journal.prepareRestore(ctx); err != nil {
		return fmt.Errorf("prepare recovery; original targets retained: %w", err)
	}
	for i := len(journal.Services) - 1; i >= 0; i-- {
		service := journal.Services[i]
		// 只要记进 journal 就停：enable --now 之后、StartedByUs 落盘之前崩溃，
		// 也必须把新进程停掉，不能因为标志没写上就宣称 rolled_back。
		if err := stopJournalService(ctx, request, service); err != nil {
			failures = append(failures, fmt.Errorf("stop %s: %w", service.Name, err))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	for i := range journal.RestoreEntries {
		if err := journal.advanceRestore(ctx, i, os.Rename); err != nil {
			return fmt.Errorf("restore interrupted; recovery journal retained at %s: %w", journal.Dir, err)
		}
	}
	if err := reloadJournalServices(ctx, request, journal.Services); err != nil {
		failures = append(failures, err)
	}
	for _, service := range journal.Services {
		// 安装前未启用的服务更需要走 restore：linuxAutostartRestore 会 disable/del，
		// 才能撤回本次安装加上的 enable / rc-update add。
		if !resumeServices {
			break
		}
		if err := restoreJournalService(ctx, request, service); err != nil {
			failures = append(failures, fmt.Errorf("restore runtime %s: %w", service.Name, err))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return journal.finishRestore()
}

// Mutations are published to memory only after the complete journal is durable.
// Slices must be detached: copying the struct alone can alter committed entries.
func (journal *rollbackJournal) nextState() rollbackJournal {
	next := *journal
	next.Backups = slices.Clone(journal.Backups)
	next.Created = slices.Clone(journal.Created)
	next.Services = slices.Clone(journal.Services)
	next.RestoreEntries = slices.Clone(journal.RestoreEntries)
	return next
}

func (journal *rollbackJournal) commit(next rollbackJournal) error {
	if err := next.persist(); err != nil {
		return err
	}
	*journal = next
	return nil
}

func (journal *rollbackJournal) persist() error {
	if journal.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(journal.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicfile.Write(filepath.Join(journal.Dir, "journal.json"), data, 0o600)
}
