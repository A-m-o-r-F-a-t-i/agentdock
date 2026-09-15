//go:build windows

package installer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/updateengine"
)

func adapterHealthFixture(t *testing.T, version string) (*Store, Transaction, Request) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version})
	}))
	t.Cleanup(server.Close)
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	root := t.TempDir()
	manifest, _ := json.Marshal(map[string]any{
		"schema_version": 1, "install_root": root, "agentdock_binary": filepath.Join(root, "bin", "agentdock.exe"),
		"host": host, "port": port, "privilege_mode": "standard", "tunnel_mode": "none",
		"local_mcp_url": "http://" + server.Listener.Addr().String() + "/mcp", "install_channel": "setup",
	})
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	tx := Transaction{TransactionID: "1234567890abcdef1234567890abcdef", Action: ActionInstall,
		InstallRoot: root, RuntimeRoot: root, Platform: "windows-amd64",
		State: updateengine.StateTrial, Phase: PhaseCommit, TargetVersion: "v1.0.1", SourceVersion: "v0.8.4"}
	if err := store.WriteTransaction(tx); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteResult(Result{TransactionID: tx.TransactionID, State: tx.State, Version: tx.TargetVersion}); err != nil {
		t.Fatal(err)
	}
	return store, tx, Request{InstallRoot: root, RuntimeRoot: root, TransactionID: tx.TransactionID, RequireHealth: true}
}

func TestAdapterCommitRequiresExpectedVersionNotAnyHTTP200(t *testing.T) {
	for _, good := range []bool{true, false} {
		t.Run(strconv.FormatBool(good), func(t *testing.T) {
			version := "0.8.4"
			if good {
				version = "1.0.1"
			}
			store, _, request := adapterHealthFixture(t, version)
			budget := 500 * time.Millisecond
			if good {
				budget = 3 * time.Second
			} // Two successful samples are 500ms apart.
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			result, err := (Engine{}).commit(ctx, store, request)
			if good {
				if err != nil || result.State != updateengine.StateCommitted || !result.Healthy {
					t.Fatalf("healthy commit: %+v err=%v", result, err)
				}
			} else {
				tx, readErr := store.ReadTransaction()
				if err == nil || readErr != nil || tx.State == updateengine.StateCommitted {
					t.Fatalf("wrong HTTP200 accepted: %+v err=%v read=%v", tx, err, readErr)
				}
			}
		})
	}
}

func TestAdapterAbandonRequiresOldVersionAndMarksFailureOtherwise(t *testing.T) {
	for _, good := range []bool{true, false} {
		t.Run(strconv.FormatBool(good), func(t *testing.T) {
			version := "1.0.1"
			if good {
				version = "0.8.4"
			}
			store, _, request := adapterHealthFixture(t, version)
			budget := 500 * time.Millisecond
			if good {
				budget = 3 * time.Second
			} // Two successful samples are 500ms apart.
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			result, err := (Engine{}).abandon(ctx, store, request)
			if good {
				if err != nil || result.State != updateengine.StateRolledBack || !result.Healthy {
					t.Fatalf("healthy rollback: %+v err=%v", result, err)
				}
			} else if err == nil || result.State != updateengine.StateFailed || result.Healthy {
				t.Fatalf("rollback falsely completed: %+v err=%v", result, err)
			}
		})
	}
}

func TestAdapterRollbackPendingIsNotACompletedTransaction(t *testing.T) {
	store, tx, _ := adapterHealthFixture(t, "0.8.4")
	result, err := leaveAdapterRollbackPending(store, tx, Result{Healthy: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if result.State != updateengine.StateRollingBack || result.Healthy || !result.CompletedAt.IsZero() || current.CompletedAt != nil {
		t.Fatalf("adapter recovery was declared complete too early: %+v tx=%+v", result, current)
	}
}
