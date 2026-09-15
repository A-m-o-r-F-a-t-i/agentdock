package filelock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireRecoversFreshLockOwnedByDeadProcess(t *testing.T) {
	const deadPID = 1 << 30
	if processAlive(deadPID) {
		t.Skip("test PID is unexpectedly alive")
	}
	path := filepath.Join(t.TempDir(), "store.lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(path, ownerPrefix+strings.Repeat("c", 32))
	if err := os.WriteFile(ownerPath, []byte("1073741824\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := Acquire(ctx, path)
	if err != nil {
		t.Fatalf("recent dead-owner lock must not delay upgrade/rollback: %v", err)
	}
	release()
}
