//go:build windows

package main

import (
	"encoding/json"
	"github.com/uvwt/agentdock/internal/fs/processlock"
	"github.com/uvwt/agentdock/internal/updateengine"
	"os"
	"path/filepath"
	"testing"
)

func TestTrayRequiresWaitOnlyDetachesNormalBackgroundLaunches(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no arguments", want: false},
		{name: "background", args: []string{"--background"}, want: false},
		{name: "background case insensitive", args: []string{" --BACKGROUND "}, want: false},
		{name: "task admin", args: []string{"--task-admin", "prepare-elevated"}, want: true},
		{name: "version", args: []string{"--version"}, want: true},
		{name: "help", args: []string{"--help"}, want: true},
		{name: "background plus management argument", args: []string{"--background", "--start-core"}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := trayRequiresWait(test.args); got != test.want {
				t.Fatalf("trayRequiresWait(%q) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}

func TestCoreLaunchRequiresParentLifetimeOnlyForServiceHost(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "service host", args: []string{"service", "launch-core", "--runtime-root", `C:\AgentDock`}, want: true},
		{name: "service host case insensitive", args: []string{" SERVICE ", " LAUNCH-CORE "}, want: true},
		{name: "service status", args: []string{"service", "status"}},
		{name: "version", args: []string{"version", "--json"}},
		{name: "empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := coreLaunchRequiresParentLifetime(test.args); got != test.want {
				t.Fatalf("coreLaunchRequiresParentLifetime(%q) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}

func TestPolicyRecoveryNeverAllowsRuntimeStartup(t *testing.T) {
	for _, args := range [][]string{nil, {"service", "start"}, {"service", "launch-core"}, {"tunnel", "launch"}, {"tunnel", "start"}, {"--background"}, {"version", "--json", "--start-core"}, {"-port", "8765"}} {
		if policyRecoveryCommand(args) {
			t.Fatalf("runtime launch bypasses policy compatibility: %q", args)
		}
	}
	for _, args := range [][]string{{"version"}, {"version", "--json"}, {"service", "status"}, {"service", "stop"}, {"tunnel", "stop"}, {"install", "inspect"}, {"uninstall"}} {
		if !policyRecoveryCommand(args) {
			t.Fatalf("recovery entry blocked: %q", args)
		}
	}
}

func TestInstallerTrialRequiresLiveMatchingOwner(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "install")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	active := updateengine.ActiveVersion{SchemaVersion: 1, State: updateengine.StateTrial, TransactionID: "install-test", ActiveVersion: "v0.8.4"}
	valid := func() map[string]any {
		return map[string]any{"schema_version": 1, "action": "install", "transaction_id": "install-test", "state": "trial", "phase": "start", "target_version": "v0.8.4", "install_root": root, "runtime_root": root}
	}
	write := func(value map[string]any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "transaction.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(valid())
	if live, err := liveInstallerTrial(root, active); err != nil || live {
		t.Fatalf("unowned trial: live=%v err=%v", live, err)
	}
	lock, acquired, err := processlock.TryAcquire(filepath.Join(directory, "transaction.lock"))
	if err != nil || !acquired {
		t.Fatalf("hold fixture lock: %v", err)
	}
	defer lock.Release()
	for _, test := range []struct {
		name, key string
		value     any
		want      bool
	}{
		{name: "matching start", want: true},
		{name: "matching health", key: "phase", value: "health", want: true},
		{name: "wrong id", key: "transaction_id", value: "other"},
		{name: "wrong target", key: "target_version", value: "v0.8.3"},
		{name: "unsupported schema", key: "schema_version", value: 2},
		{name: "failed", key: "state", value: "failed"},
		{name: "uninstall", key: "action", value: "uninstall"},
		{name: "adapter commit wait", key: "phase", value: "commit"},
		{name: "wrong install root", key: "install_root", value: filepath.Join(root, "other")},
		{name: "wrong runtime root", key: "runtime_root", value: filepath.Join(root, "other")},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := valid()
			if test.key != "" {
				value[test.key] = test.value
			}
			write(value)
			live, err := liveInstallerTrial(root, active)
			if err != nil || live != test.want {
				t.Fatalf("live=%v want=%v err=%v", live, test.want, err)
			}
		})
	}
	write(valid())
	store, err := updateengine.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteActive(active); err != nil {
		t.Fatal(err)
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveActiveWithRecovery(root, store, layout); err == nil {
		t.Fatal("ordinary shim command must not enter an installer trial")
	}
	if got, err := resolveActiveWithRecovery(root, store, layout, true); err != nil || got.TransactionID != active.TransactionID {
		t.Fatalf("live service host: %+v %v", got, err)
	}
}
