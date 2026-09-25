package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func TestCommandCapacityRefusesBeforeProcessAndReturnsCredits(t *testing.T) {
	svc, cfg := newCommandTestService(t)
	store, err := activity.New(t.TempDir(), activity.Options{AppendEvents: 2, AppendBytes: 2 * activity.MaxEventBytes})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetActivityStore(store)
	marker := filepath.Join(cfg.AgentDockDefaultDir, "must-not-start.txt")
	cmd := "printf unexpected > '" + marker + "'"
	if runtime.GOOS == "windows" {
		cmd = "Set-Content -LiteralPath '" + marker + "' -Value unexpected"
	}
	occupied, err := store.ReserveAppend(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.execArgs(context.Background(), map[string]any{"cmd": cmd, "execution_mode": "sync"})
	var failure *ToolError
	if !errors.As(err, &failure) || failure.Code != "ACTIVITY_CAPACITY" {
		t.Fatalf("command capacity error: %v", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("refused command had a side effect")
	}
	occupied.Close()
	result, err := svc.execArgs(context.Background(), map[string]any{"cmd": "echo capacity-recovered", "execution_mode": "sync"})
	if err != nil || result["command_ok"] != true {
		t.Fatalf("normal command failed after refusal: %v %v", result, err)
	}
	svc.activityWG.Wait()
	if stats := store.AppendStatistics(); stats.ReservedEvents != 0 || stats.CommittedEvents < 2 {
		t.Fatalf("command lifecycle reservation leaked: %+v", stats)
	}
}
