package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	readerOwnerPrefix       = "reader-"
	readerStaleAfter        = 25 * time.Hour
	writerStaleAfter        = 10 * time.Minute
	writerHeartbeatInterval = time.Minute
)

func (s *Store) AcquireRead(ctx context.Context, skill string) (func(), error) {
	return s.acquireRead(ctx, strings.TrimSpace(skill))
}

func (s *Store) AcquireWrite(ctx context.Context, skill string) (func(), error) {
	return s.acquireWrite(ctx, strings.TrimSpace(skill))
}

func (s *Store) acquireRead(ctx context.Context, skill string) (func(), error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return nil, err
	}
	readers, writer, err := s.componentLockPaths(skill)
	if err != nil {
		return nil, err
	}
	owner, err := newLockOwner()
	if err != nil {
		return nil, fmt.Errorf("create Skill reader owner: %w", err)
	}
	readerPath := filepath.Join(readers, readerOwnerPrefix+owner)
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("acquire Skill read lock: %w", err)
		}
		if _, err := os.Stat(writer); err == nil {
			if err := waitForLockRetry(ctx, ticker); err != nil {
				return nil, err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect Skill writer lock: %w", err)
		}

		file, err := os.OpenFile(readerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create Skill reader lock: %w", err)
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(readerPath)
			return nil, fmt.Errorf("close Skill reader lock: %w", closeErr)
		}

		if _, err := os.Stat(writer); errors.Is(err, os.ErrNotExist) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = os.Remove(readerPath)
				return nil, fmt.Errorf("acquire Skill read lock: %w", ctxErr)
			}
			return func() {
				if err := os.Remove(readerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					slog.Warn("release Skill reader lock failed", "path", readerPath, "error", err)
				}
			}, nil
		} else if err != nil {
			_ = os.Remove(readerPath)
			return nil, fmt.Errorf("recheck Skill writer lock: %w", err)
		}

		_ = os.Remove(readerPath)
		if err := waitForLockRetry(ctx, ticker); err != nil {
			return nil, err
		}
	}
}

func (s *Store) acquireWrite(ctx context.Context, skill string) (func(), error) {
	if err := validateIdentifier("skill", skill); err != nil {
		return nil, err
	}
	readers, writer, err := s.componentLockPaths(skill)
	if err != nil {
		return nil, err
	}
	owner, err := newLockOwner()
	if err != nil {
		return nil, fmt.Errorf("create Skill writer owner: %w", err)
	}
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()
	var transientErrorSince time.Time

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("acquire Skill write lock: %w", err)
		}
		err := os.Mkdir(writer, 0o700)
		if err == nil {
			ownerPath := filepath.Join(writer, lockOwnerPrefix+owner)
			if err := os.WriteFile(ownerPath, nil, 0o600); err != nil {
				cleanupErr := cleanupOwnedLockInitialization(writer, ownerPath)
				return nil, errors.Join(fmt.Errorf("write Skill writer owner: %w", err), cleanupErr)
			}
			break
		}
		if errors.Is(err, os.ErrExist) {
			transientErrorSince = time.Time{}
			if info, statErr := os.Stat(writer); statErr == nil && time.Since(info.ModTime()) > writerStaleAfter {
				if removeStaleOwnedLock(writer) {
					continue
				}
			}
		} else if isTransientLockContention(err) {
			if transientErrorSince.IsZero() {
				transientErrorSince = time.Now()
			} else if time.Since(transientErrorSince) >= transientLockErrorRetryTime {
				return nil, fmt.Errorf("acquire Skill writer lock: %w", err)
			}
		} else {
			return nil, fmt.Errorf("acquire Skill writer lock: %w", err)
		}
		if err := waitForLockRetry(ctx, ticker); err != nil {
			return nil, err
		}
	}

	release := func() { releaseOwnedLock(writer, owner) }
	nextHeartbeat := time.Now().Add(writerHeartbeatInterval)
	for {
		empty, err := readersEmpty(readers)
		if err != nil {
			release()
			return nil, err
		}
		if empty {
			if ctxErr := ctx.Err(); ctxErr != nil {
				release()
				return nil, fmt.Errorf("acquire Skill write lock: %w", ctxErr)
			}
			return release, nil
		}
		if !time.Now().Before(nextHeartbeat) {
			if err := refreshOwnedLock(writer, owner); err != nil {
				release()
				return nil, fmt.Errorf("refresh Skill writer lock: %w", err)
			}
			nextHeartbeat = time.Now().Add(writerHeartbeatInterval)
		}
		cleanupStaleReaders(readers)
		if err := waitForLockRetry(ctx, ticker); err != nil {
			release()
			return nil, err
		}
	}
}

func readersEmpty(readers string) (bool, error) {
	entries, err := os.ReadDir(readers)
	if err != nil {
		return false, fmt.Errorf("read Skill reader locks: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), readerOwnerPrefix) {
			return false, nil
		}
	}
	return true, nil
}

func cleanupStaleReaders(readers string) {
	entries, err := os.ReadDir(readers)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), readerOwnerPrefix) {
			continue
		}
		path := filepath.Join(readers, entry.Name())
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > readerStaleAfter {
			_ = os.Remove(path)
		}
	}
}

func waitForLockRetry(ctx context.Context, ticker *time.Ticker) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("acquire Skill lock: %w", ctx.Err())
	case <-ticker.C:
		return nil
	}
}

func refreshOwnedLock(lockPath, owner string) error {
	ownerPath := filepath.Join(lockPath, lockOwnerPrefix+owner)
	info, err := os.Lstat(ownerPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Skill lock owner is not a regular file")
	}
	now := time.Now()
	return os.Chtimes(lockPath, now, now)
}
func (s *Store) componentLockPaths(skill string) (readers, writer string, err error) {
	if err = validateIdentifier("skill", skill); err != nil {
		return
	}
	readers = filepath.Join(s.root, locksDirectory, skill+".readers")
	err = os.MkdirAll(readers, 0700)
	writer = filepath.Join(s.root, locksDirectory, skill+".lock")
	return
}
