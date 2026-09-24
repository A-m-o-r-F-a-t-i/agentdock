//go:build !windows

package command

import (
	"runtime"
	"testing"
)

func TestNonWindowsExecCommandRejectsRuntimeOverride(t *testing.T) {
	service, _ := newCommandTestService(t)
	_, err := service.prepareCommandInvocationArgs(map[string]any{"runtime": "wsl"}, "pwd")
	if err == nil {
		t.Fatal("expected non-Windows runtime override to be rejected")
	}
}

func TestNativeCommandRecordsItsResolvedExecutionContext(t *testing.T) {
	svc, cfg := newCommandTestService(t)
	result, err := svc.Exec(t.Context(), ExecRequest{Cmd: "printf metadata", ExecutionMode: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	if result["runtime"] != runtime.GOOS || result["workdir"] != cfg.AgentDockDefaultDir {
		t.Fatalf("native execution metadata lost: %+v", result)
	}
}
