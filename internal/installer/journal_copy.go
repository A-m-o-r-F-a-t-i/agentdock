package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Backup budgets reject the whole snapshot; they never silently truncate it.
const (
	backupMaxBytes   int64 = 2 << 30
	backupMaxEntries       = 100000
	backupMaxDepth         = 128
)

type backupDirectoryMode struct {
	path string
	mode fs.FileMode
}
type backupContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// A destination of "" computes an integrity digest without modifying files.
// Installation material intentionally keeps its separate default-mode copier.
func copyBackupTree(ctx context.Context, source, destination string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := sha256.New()
	var bytes int64
	var entries int
	var directories []backupDirectoryMode
	type metadataChange struct {
		path     string
		metadata *backupNativeMetadata
	}
	var nativeChanges []metadataChange
	metadataBytes := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("backup does not follow links or special files: %s", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entries++; entries > backupMaxEntries || strings.Count(relative, string(filepath.Separator)) > backupMaxDepth {
			return errors.New("backup entry or depth budget exceeded")
		}
		native, err := readBackupNativeMetadata(path)
		if err != nil {
			return fmt.Errorf("backup metadata for %s: %w", path, err)
		}
		metadataBytes += backupNativeMetadataSize(native)
		if metadataBytes > 32<<20 {
			return errors.New("backup metadata budget exceeded")
		}
		target := ""
		if destination != "" {
			target = filepath.Join(destination, relative)
			if native != nil {
				nativeChanges = append(nativeChanges, metadataChange{target, native})
			}
		}
		size := int64(0)
		digest := ""
		if info.IsDir() {
			if target != "" {
				if err := os.Mkdir(target, 0o700); err != nil {
					return err
				}
				directories = append(directories, backupDirectoryMode{target, info.Mode().Perm()})
			}
		} else {
			if info.Size() > backupMaxBytes-bytes {
				return errors.New("backup byte budget exceeded")
			}
			digest, size, err = copyBackupFile(ctx, path, target, info, backupMaxBytes-bytes)
			if err != nil {
				return err
			}
			bytes += size
		}
		// JSON escapes path separators/control characters unambiguously.
		record, err := json.Marshal(struct {
			Path      string                `json:"path"`
			Mode      uint32                `json:"mode"`
			Directory bool                  `json:"directory"`
			Bytes     int64                 `json:"bytes"`
			Digest    string                `json:"digest"`
			Native    *backupNativeMetadata `json:"native,omitempty"`
		}{filepath.ToSlash(relative), uint32(info.Mode().Perm()), info.IsDir(), size, digest, native})
		if err != nil {
			return err
		}
		_, _ = hash.Write(record)
		_, _ = hash.Write([]byte{'\n'})
		return nil
	})
	if err != nil {
		return "", err
	}
	// Apply directory modes only after all children have been written.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return "", err
		}
	}
	// Restore ancestors before descendants so inherited Windows permissions do
	// not replace the recorded child descriptor. The private stage ancestor
	// still protects snapshots while original rights are restored below it.
	for _, change := range nativeChanges {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := applyBackupNativeMetadata(change.path, change.metadata); err != nil {
			return "", err
		}
	}
	for _, change := range nativeChanges {
		actual, err := readBackupNativeMetadata(change.path)
		if err != nil || !equalBackupNativeMetadata(actual, change.metadata) {
			return "", fmt.Errorf("native backup metadata was not preserved: %s: %w", change.path, errors.Join(err, errors.New(backupNativeMetadataDifference(actual, change.metadata))))
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyBackupFile(ctx context.Context, source, destination string, before fs.FileInfo, remaining int64) (string, int64, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return "", 0, errors.New("backup source changed before opening")
	}
	digest := sha256.New()
	var output *os.File
	var writer io.Writer = digest
	if destination != "" {
		output, err = os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", 0, err
		}
		defer output.Close()
		writer = io.MultiWriter(digest, output)
	}
	count, err := io.CopyBuffer(writer, backupContextReader{ctx, io.LimitReader(input, remaining+1)}, make([]byte, 64<<10))
	if err != nil {
		return "", count, err
	}
	if count > remaining {
		return "", count, errors.New("backup byte budget exceeded")
	}
	after, err := input.Stat()
	if err != nil {
		return "", count, err
	}
	if count != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", count, errors.New("backup source changed during copying")
	}
	if output != nil {
		if err := output.Chmod(before.Mode().Perm()); err != nil {
			return "", count, err
		}
		if err := output.Sync(); err != nil {
			return "", count, err
		}
		if err := output.Close(); err != nil {
			return "", count, err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), count, nil
}
