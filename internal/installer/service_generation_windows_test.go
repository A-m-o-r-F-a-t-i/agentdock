//go:build windows

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/desktopruntime"
	"github.com/uvwt/agentdock/internal/fs/processlock"
	"github.com/uvwt/agentdock/internal/updateengine"
)

// This exercises the real Core and shim without installing Setup, registering a
// task, launching a Core server, or touching the user's runtime or credentials.
func TestWindowsInstallerControlsTrialThroughGenerationCore(t *testing.T) {
	root := t.TempDir()
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	version := "v1.1.6"
	core := layout.GenerationCore(version)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, target := range []struct{ output, source string }{
		{core, "./cmd/agentdock"},
		{layout.CoreShim(), "./cmd/agentdock-shim"},
	} {
		if err := os.MkdirAll(filepath.Dir(target.output), 0700); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go.exe"),
			"build", "-p", "2", "-o", target.output, target.source)
		command.Dir = filepath.Join("..", "..")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", target.source, err, output)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": "1.1.6"})
	}))
	defer server.Close()
	port, err := strconv.Atoi(strings.Split(server.Listener.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := desktopruntime.Manifest{
		SchemaVersion: desktopruntime.SchemaVersion, InstallRoot: root,
		AgentDockHome: home, AgentDockDefaultDir: root, AgentDockBinary: layout.CoreShim(),
		PrivilegeMode: "standard", StartupValueName: "AgentDock-Installer-Regression",
		Host: "127.0.0.1", Port: port, LocalMCPURL: server.URL + "/mcp",
		TunnelMode: "none", InstallChannel: "setup",
	}
	if err := desktopruntime.Save(filepath.Join(root, "runtime.json"), manifest); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	transaction := Transaction{
		SchemaVersion: SchemaVersion, TransactionID: "1234567890abcdef1234567890abcdef",
		Action: ActionInstall, State: updateengine.StateTrial, Phase: PhaseStart,
		InstallRoot: root, RuntimeRoot: root, Platform: "windows", TargetVersion: version,
	}
	if err := store.WriteTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	lock, err := processlock.Acquire(ctx, store.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	writeWindowsTrialPointer(t, root, version, transaction.TransactionID)
	pointerPath := filepath.Join(root, "active-version.json")
	before, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}

	// An ordinary user command must remain blocked even while the installer owns
	// the trial; the repair must not broaden the shim's startup authorization.
	for _, action := range []string{"start", "status", "stop"} {
		command := exec.CommandContext(ctx, layout.CoreShim(), "service", action, "--runtime-root", root)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "active generation is still a trial") {
			t.Fatalf("ordinary shim service %s: err=%v output=%s", action, err, output)
		}
	}

	request := Request{InstallRoot: root, RuntimeRoot: root, Version: version, StartService: true}
	journal := newJournal(root, transaction.TransactionID)
	if err := startWindowsServices(ctx, request, journal); err != nil {
		t.Fatalf("installer trial service start: %v", err)
	}
	if len(journal.Services) != 1 || !journal.Services[0].StartedByUs {
		t.Fatalf("successful start was not journaled: %+v", journal.Services)
	}
	if _, err := probeWindowsComponentRunning(ctx, windowsServiceBinary(request), "service", root); err != nil {
		t.Fatalf("installer trial service status: %v", err)
	}
	if err := stopJournalService(ctx, request, journal.Services[0]); err != nil {
		t.Fatalf("installer trial rollback stop: %v", err)
	}
	after, err := os.ReadFile(pointerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("service management changed the uncommitted pointer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "update", "transaction.json")); !os.IsNotExist(err) {
		t.Fatalf("installer service management must not create a self-update transaction: %v", err)
	}
}
