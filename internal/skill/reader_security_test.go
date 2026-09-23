package skill

import (
	"context"
	"errors"
	skillstate "github.com/uvwt/agentdock/internal/skill/state"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testReaderFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	state, err := skillstate.New(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: reader-skill\ndescription: Reader fixture.\nversion: 1.0.0\n---\n\n# Reader\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return manager, source
}
func TestInstallRejectsFIFOWithoutTryingToHashIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX FIFO is tested on Linux CI")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	manager, source := testReaderFixture(t)
	if out, err := exec.Command(mkfifo, filepath.Join(source, "blocked-pipe")).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = manager.Install(ctx, InstallRequest{Source: source})
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("FIFO must be rejected before hashing: %v", err)
	}
}
func TestVersionedUninstallWaitsForActiveReader(t *testing.T) {
	manager, source := testReaderFixture(t)
	if _, err := manager.Install(t.Context(), InstallRequest{Source: source, Activate: true}); err != nil {
		t.Fatal(err)
	}
	release, err := manager.State.AcquireRead(t.Context(), "reader-skill")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() { _, err := manager.Uninstall(ctx, "reader-skill", ""); done <- err }()
	select {
	case err := <-done:
		release()
		t.Fatalf("uninstall ran during active reader: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(manager.State.Root(), "reader-skill")); !os.IsNotExist(err) {
		t.Fatalf("uninstall did not commit: %v", err)
	}
}
