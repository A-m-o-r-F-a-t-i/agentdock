package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/bundledrg"
)

func TestRGSelectionFallbackAndIntegrityBoundary(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "agentdock-core.exe")
	calls := 0
	lookup := func(name string) (string, error) {
		calls++
		if name != "rg" {
			t.Fatalf("unexpected lookup: %s", name)
		}
		return filepath.Join(root, "allowed-path-rg.exe"), nil
	}
	selection, err := selectRGForExecutable(context.Background(), executable, "windows", "amd64", lookup)
	if err != nil || selection.source != "path" || calls != 1 {
		t.Fatalf("missing bundle must use allowed PATH: %+v, %v, %d", selection, err, calls)
	}
	if err := os.MkdirAll(filepath.Join(root, "share", "agentdock", "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := selectRGForExecutable(context.Background(), executable, "windows", "amd64", lookup); !errors.Is(err, bundledrg.ErrIntegrity) || calls != 0 {
		t.Fatalf("partial bundle silently downgraded: %v, lookup=%d", err, calls)
	}
	for _, platform := range [][2]string{{"windows", "arm64"}, {"linux", "amd64"}, {"darwin", "arm64"}} {
		selection, err = selectRGForExecutable(context.Background(), executable, platform[0], platform[1], lookup)
		if err != nil || selection.source != "path" {
			t.Fatalf("unsupported platform changed: %v %v", platform, err)
		}
	}
	for _, absent := range []error{exec.ErrNotFound, exec.ErrDot, os.ErrNotExist} {
		selection, err = selectRGForExecutable(context.Background(), executable, "linux", "amd64", func(string) (string, error) { return "", absent })
		if err != nil || selection.path != "" {
			t.Fatalf("unavailable/disallowed PATH must permit Go fallback: %+v %v", selection, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	if _, err := selectRGForExecutable(ctx, executable, "windows", "amd64", lookup); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled selection has side effects: %v", err)
	}
}

func TestRGOutputBudgetCancelsWithoutGrowingPastLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := rgBoundedOutput{limit: 4, cancel: cancel}
	n, err := output.Write([]byte("123456"))
	if n != 4 || !errors.Is(err, errSearchResourceLimit) || !output.exceeded || output.Len() != 4 || ctx.Err() == nil {
		t.Fatalf("unbounded output: n=%d err=%v len=%d", n, err, output.Len())
	}
}
