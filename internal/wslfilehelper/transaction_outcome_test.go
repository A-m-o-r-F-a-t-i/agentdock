//go:build linux

package wslfilehelper

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransactionOutcomeConfirmsCompleteRollback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := "after\n"
	req := existingTransactionChange(t, target, &updated)
	ops := defaultTransactionOps()
	realRename := ops.renameNoReplace
	ops.renameNoReplace = func(source, dest string) error {
		if strings.Contains(filepath.Base(source), "patch-write") {
			return errors.New("injected install error")
		}
		return realRename(source, dest)
	}
	_, err := patchTransaction(&Request{Action: "patch_transaction", Workdir: root, Changes: []ChangeRequest{req}}, ops)
	if err == nil || failureFrom(err).Details["edit_outcome"] != "unchanged" {
		t.Fatalf("unverified rollback marker: %#v", failureFrom(err))
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "before\n" {
		t.Fatal("rollback bytes", readErr)
	}
}

func TestTransactionOutcomeReportsConfirmedResiduals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	for _, target := range []string{a, b} {
		if err := os.WriteFile(target, []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	updated := "after\n"
	changes := []ChangeRequest{existingTransactionChange(t, a, &updated), existingTransactionChange(t, b, &updated)}
	ops := defaultTransactionOps()
	realRename := ops.renameNoReplace
	ops.renameNoReplace = func(source, dest string) error {
		if dest == b && (strings.Contains(filepath.Base(source), "patch-write") || strings.Contains(filepath.Base(source), "patch-backup")) {
			return errors.New("injected install and restore error")
		}
		return realRename(source, dest)
	}
	_, err := patchTransaction(&Request{Action: "patch_transaction", Workdir: root, Changes: changes}, ops)
	if err == nil {
		t.Fatal("fault succeeded")
	}
	failure := failureFrom(err)
	if failure.Code != "PATCH_ROLLBACK_INCOMPLETE" || failure.Details["edit_outcome"] != "unknown" {
		t.Fatal(failure)
	}
	outcomes, ok := failure.Details["residual_outcomes"].(map[string]string)
	if !ok || outcomes[a] != "unchanged" || outcomes[b] != "deleted" {
		t.Fatalf("residuals=%#v", failure.Details)
	}
}

func TestTransactionNewNestedScopeKeepsRecoveryIdentity(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	scope := filepath.Join(root, "new", "nested")
	target := filepath.Join(scope, "a.txt")
	before, err := transactionStateDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	req := newTransactionChange(target, "hello\n", 0o644)
	response, err := patchTransaction(&Request{Action: "patch_transaction", Workdir: scope, Changes: []ChangeRequest{req}}, defaultTransactionOps())
	if err != nil || response.FilesChanged == nil || *response.FilesChanged != 1 {
		t.Fatal("nested add", response, err)
	}
	after, err := transactionStateDir(scope)
	if err != nil || after != before {
		t.Fatal("journal scope changed", before, after, err)
	}
	if _, err := recoverPatchTransactions(&Request{Workdir: scope}, defaultTransactionOps()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "hello\n" {
		t.Fatal("nested bytes", err)
	}
}

func TestTransactionCleanupDurabilityErrorDoesNotReportBusinessFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	stateDir, err := transactionStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	req := newTransactionChange(target, "written\n", 0o644)
	ops := defaultTransactionOps()
	realSync := ops.fsyncDirStrict
	syncs := 0
	ops.fsyncDirStrict = func(path string) error {
		if path == stateDir {
			syncs++
			if syncs == 4 {
				return errors.New("injected post-commit directory sync failure")
			}
		}
		return realSync(path)
	}
	response, err := patchTransaction(&Request{Action: "patch_transaction", Workdir: root, Changes: []ChangeRequest{req}}, ops)
	if err != nil || response == nil || response.CleanupPending == nil || !*response.CleanupPending {
		t.Fatal("committed edit reported failure", response, err, syncs)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "written\n" {
		t.Fatal("committed bytes changed", err)
	}
}
