package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
)

type journalRestoreEntry struct {
	Original   string `json:"original"`
	Backup     string `json:"backup,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Existed    bool   `json:"existed"`
	Stage      string `json:"stage"`
	Phase      string `json:"phase"`
	HadCurrent bool   `json:"had_current"`
}

func restorePathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
func restoreWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
func restoreExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
func validateRestorePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == filepath.Dir(filepath.Clean(path)) {
		return fmt.Errorf("unsafe restore target: %q", path)
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || current != filepath.Clean(path) && !info.IsDir()) {
			return fmt.Errorf("restore refuses link or non-directory ancestor: %s", current)
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func (j *rollbackJournal) restoreTargets() ([]journalRestoreEntry, error) {
	targets := []journalRestoreEntry{}
	seen := map[string]bool{}
	// A parent backup already owns the removal of newly-created descendants.
	for i := len(j.Created) - 1; i >= 0; i-- {
		path := j.Created[i]
		covered := false
		for _, backup := range j.Backups {
			if restoreWithin(path, backup.Original) {
				covered = true
				break
			}
		}
		if covered || seen[restorePathKey(path)] {
			continue
		}
		seen[restorePathKey(path)] = true
		targets = append(targets, journalRestoreEntry{Original: filepath.Clean(path)})
	}
	for i := len(j.Backups) - 1; i >= 0; i-- {
		backup := j.Backups[i]
		if seen[restorePathKey(backup.Original)] {
			return nil, fmt.Errorf("duplicate restore target: %s", backup.Original)
		}
		seen[restorePathKey(backup.Original)] = true
		targets = append(targets, journalRestoreEntry{Original: filepath.Clean(backup.Original), Backup: backup.Backup, Digest: backup.Digest, Existed: backup.Existed})
	}
	for _, target := range targets {
		if err := validateRestorePath(target.Original); err != nil {
			return nil, err
		}
		if restoreWithin(j.Dir, target.Original) || restoreWithin(target.Original, j.Dir) {
			return nil, errors.New("restore target overlaps its recovery journal")
		}
		if target.Existed {
			if !restoreWithin(target.Backup, j.Dir) || target.Backup == j.Dir || !filepath.IsAbs(target.Backup) {
				return nil, errors.New("backup is outside its transaction directory")
			}
			if len(target.Digest) != 64 {
				return nil, fmt.Errorf("backup has no supported integrity record; preserve the legacy backup for manual recovery: %s", target.Backup)
			}
		}
	}
	return targets, nil
}

func (j *rollbackJournal) stageOwner(entry journalRestoreEntry) []byte {
	data, _ := json.Marshal(struct{ Transaction, Target, Stage string }{j.TransactionID, entry.Original, entry.Stage})
	return data
}
func (j *rollbackJournal) validateStage(entry journalRestoreEntry) error {
	if !filepath.IsAbs(entry.Stage) || !strings.HasPrefix(filepath.Base(entry.Stage), ".agentdock-restore-") ||
		!restoreWithin(entry.Original, filepath.Dir(entry.Stage)) || restoreWithin(entry.Stage, entry.Original) {
		return errors.New("invalid restoration staging location")
	}
	if err := validateRestorePath(entry.Stage); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(entry.Stage, "owner.json"))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32768 {
		return errors.New("invalid restoration owner marker")
	}
	owner, err := os.ReadFile(filepath.Join(entry.Stage, "owner.json"))
	if err != nil {
		return err
	}
	if string(owner) != string(j.stageOwner(entry)) {
		return errors.New("restoration staging owner does not match")
	}
	return nil
}
func (j *rollbackJournal) updateRestore(index int, entry journalRestoreEntry) error {
	next := j.nextState()
	next.RestoreEntries[index] = entry
	return j.commit(next)
}

func (j *rollbackJournal) prepareRestore(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.RestoreStatus != "" && j.RestoreStatus != "preparing" && j.RestoreStatus != "prepared" {
		return errors.New("unsupported recovery journal status")
	}
	targets, err := j.restoreTargets()
	if err != nil {
		return err
	}
	if j.RestoreStatus == "" {
		// Stage outside every affected subtree, but on the target's volume.
		for i := range targets {
			parent := filepath.Dir(targets[i].Original)
			for {
				info, statErr := os.Stat(parent)
				covered := false
				for _, target := range targets {
					if restoreWithin(parent, target.Original) {
						covered = true
						break
					}
				}
				if statErr == nil && info.IsDir() && !covered {
					break
				}
				if statErr != nil && !os.IsNotExist(statErr) {
					return statErr
				}
				if parent == filepath.Dir(parent) {
					return errors.New("cannot prepare a same-volume restore stage")
				}
				parent = filepath.Dir(parent)
			}
			stage, err := os.MkdirTemp(parent, ".agentdock-restore-")
			if err != nil {
				return err
			}
			targets[i].Stage, targets[i].Phase = stage, "preparing"
			if err := securepath.EnsurePrivate(stage); err != nil {
				return err
			}
			if err := atomicfile.Write(filepath.Join(stage, "owner.json"), j.stageOwner(targets[i]), 0o600); err != nil {
				return err
			}
		}
		next := j.nextState()
		next.RestoreEntries = targets
		next.RestoreStatus = "preparing"
		if err := j.commit(next); err != nil {
			return err
		}
	}
	if len(j.RestoreEntries) != len(targets) {
		return errors.New("restore target set changed")
	}
	for i, entry := range j.RestoreEntries {
		target := targets[i]
		if entry.Original != target.Original || entry.Backup != target.Backup || entry.Digest != target.Digest || entry.Existed != target.Existed {
			return errors.New("restore entry does not match the backup journal")
		}
		if err := j.validateStage(entry); err != nil {
			return err
		}
		switch entry.Phase {
		case "preparing", "prepared", "moving_current", "current_saved", "installing", "verified":
		default:
			return fmt.Errorf("unsupported recovery phase %q", entry.Phase)
		}
		if entry.Phase != "preparing" {
			// A prepared copy may have changed since a previous process exited.
			// Verify it before this attempt moves any still-current target.
			if entry.Existed && (entry.Phase == "prepared" || entry.Phase == "moving_current" || entry.Phase == "current_saved") {
				digest, err := copyBackupTree(ctx, filepath.Join(entry.Stage, "prepared"), "")
				if err != nil {
					return fmt.Errorf("verify retained preparation: %w", err)
				}
				if digest != entry.Digest {
					return errors.New("retained preparation failed integrity verification")
				}
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Existed {
			if err := validateRestorePath(entry.Backup); err != nil {
				return err
			}
			prepared := filepath.Join(entry.Stage, "prepared")
			// Only uncommitted preparation data may be discarded and recopied.
			if err := os.RemoveAll(prepared); err != nil {
				return err
			}
			digest, err := copyBackupTree(ctx, entry.Backup, prepared)
			if err != nil {
				return fmt.Errorf("prepare %s: %w", entry.Original, err)
			}
			if digest != entry.Digest {
				return fmt.Errorf("backup integrity mismatch: %s", entry.Backup)
			}
			verified, err := copyBackupTree(ctx, prepared, "")
			if err != nil || verified != entry.Digest {
				return fmt.Errorf("prepared backup verification failed for %s: %w", entry.Original, errors.Join(err, errors.New("digest mismatch")))
			}
		}
		entry.Phase = "prepared"
		if err := j.updateRestore(i, entry); err != nil {
			return err
		}
	}
	if j.RestoreStatus == "preparing" {
		next := j.nextState()
		next.RestoreStatus = "prepared"
		return j.commit(next)
	}
	return nil
}

// The move seam permits deterministic crash-boundary tests. Production always
// supplies os.Rename; no global mutable fault-injection state is involved.
func (j *rollbackJournal) advanceRestore(ctx context.Context, index int, move func(string, string) error) error {
	entry := j.RestoreEntries[index]
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.Phase == "verified" {
		return nil
	}
	if err := j.validateStage(entry); err != nil {
		return err
	}
	if err := validateRestorePath(entry.Original); err != nil {
		return err
	}
	prepared, previous := filepath.Join(entry.Stage, "prepared"), filepath.Join(entry.Stage, "previous")
	if entry.Phase == "prepared" {
		exists, err := restoreExists(entry.Original)
		if err != nil {
			return err
		}
		entry.HadCurrent, entry.Phase = exists, "moving_current"
		if err := j.updateRestore(index, entry); err != nil {
			return err
		}
	}
	if entry.Phase == "moving_current" {
		current, err := restoreExists(entry.Original)
		if err != nil {
			return err
		}
		saved, err := restoreExists(previous)
		if err != nil {
			return err
		}
		if entry.HadCurrent {
			if saved && current || !saved && !current {
				return errors.New("ambiguous current-target move; recovery materials retained")
			}
			if !saved {
				if err := move(entry.Original, previous); err != nil {
					return err
				}
			}
		} else if current || saved {
			return errors.New("unexpected target appeared during recovery")
		}
		entry.Phase = "current_saved"
		if err := j.updateRestore(index, entry); err != nil {
			return err
		}
	}
	if entry.Phase == "current_saved" {
		entry.Phase = "installing"
		if err := j.updateRestore(index, entry); err != nil {
			return err
		}
	}
	if entry.Phase == "installing" {
		current, err := restoreExists(entry.Original)
		if err != nil {
			return err
		}
		if entry.Existed {
			staged, err := restoreExists(prepared)
			if err != nil {
				return err
			}
			if staged && current || !staged && !current {
				return errors.New("ambiguous replacement; recovery materials retained")
			}
			if staged {
				if err := os.MkdirAll(filepath.Dir(entry.Original), 0o755); err != nil {
					return err
				}
				if err := move(prepared, entry.Original); err != nil {
					return err
				}
			}
			digest, err := copyBackupTree(ctx, entry.Original, "")
			if err != nil {
				return err
			}
			if digest != entry.Digest {
				return errors.New("restored target did not pass integrity verification")
			}
		} else if current {
			return errors.New("new target was not removed")
		}
		entry.Phase = "verified"
		return j.updateRestore(index, entry)
	}
	return fmt.Errorf("unsupported recovery phase %q", entry.Phase)
}

func (j *rollbackJournal) finishRestore() error {
	for _, entry := range j.RestoreEntries {
		if entry.Phase != "verified" {
			return errors.New("cannot discard an incomplete recovery")
		}
	}
	if j.RestoreStatus != "complete" {
		next := j.nextState()
		next.RestoreStatus = "complete"
		if err := j.commit(next); err != nil {
			return err
		}
	}
	for _, entry := range j.RestoreEntries {
		exists, err := restoreExists(entry.Stage)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := j.validateStage(entry); err != nil {
			return err
		}
		if err := os.RemoveAll(entry.Stage); err != nil {
			return fmt.Errorf("restored state verified; cleanup retained at %s: %w", entry.Stage, err)
		}
	}
	return nil
}
