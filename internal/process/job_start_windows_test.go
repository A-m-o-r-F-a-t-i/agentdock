//go:build windows

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

func jobFixtureEnv(mode, root string) []string {
	return append(os.Environ(), "AGENTDOCK_JOB_TEST_MODE="+mode, "AGENTDOCK_JOB_TEST_ROOT="+root)
}
func jobFixtureIdentity(handle windows.Handle, pid uint32) JobChild {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(handle, &c, &e, &k, &u); err != nil {
		panic(err)
	}
	return JobChild{Handle: handle, PID: pid, Created: uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime)}
}
func TestCreateTimeJobHelperProcess(t *testing.T) {
	mode := os.Getenv("AGENTDOCK_JOB_TEST_MODE")
	if mode == "" {
		return
	}
	root := os.Getenv("AGENTDOCK_JOB_TEST_ROOT")
	switch mode {
	case "leaf":
		var inJob int32
		proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
		ok, _, err := proc.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
		if ok == 0 || inJob == 0 {
			t.Fatalf("first instruction was outside a Job: %v", err)
		}
		current := jobFixtureIdentity(windows.CurrentProcess(), uint32(os.Getpid()))
		data, _ := json.Marshal(current)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("leaf-%d.json", current.PID)), data, 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "tree":
		child := exec.Command(os.Args[0], "-test.run=^TestCreateTimeJobHelperProcess$")
		child.Env = jobFixtureEnv("leaf", root)
		Configure(child)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		_ = child.Wait()
	case "host":
		job, err := NewJob()
		if err != nil {
			t.Fatal(err)
		}
		defer job.Close()
		null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer null.Close()
		child, err := job.Start(os.Args[0], []string{"-test.run=^TestCreateTimeJobHelperProcess$"}, root, jobFixtureEnv("tree", root), null, null, null)
		if err != nil {
			t.Fatal(err)
		}
		defer child.Close()
		data, _ := json.Marshal(child)
		if err := os.WriteFile(filepath.Join(root, "child.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		_, _ = child.Wait(context.Background())
	default:
		t.Fatal("invalid helper mode")
	}
}
func waitJobFixture(t *testing.T, pattern string) JobChild {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := filepath.Glob(pattern)
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var original JobChild
			if json.Unmarshal(data, &original) != nil {
				continue
			}
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, original.PID)
			if err != nil {
				t.Fatal(err)
			}
			captured := jobFixtureIdentity(handle, original.PID)
			if captured.Created != original.Created {
				windows.CloseHandle(handle)
				t.Fatal("fixture PID reused")
			}
			return captured
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("helper did not publish original process identity")
	return JobChild{}
}
func TestCreateTimeJobOwnsCoreReplacementAndDescendants(t *testing.T) {
	root := t.TempDir()
	job, err := NewJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	start := func(mode string) *JobChild {
		t.Helper()
		child, err := job.Start(os.Args[0], []string{"-test.run=^TestCreateTimeJobHelperProcess$"}, root, jobFixtureEnv(mode, root), null, null, null)
		if err != nil {
			t.Fatal(err)
		}
		return child
	}
	supervisor := start("tree")
	defer supervisor.Close()
	grandchild := waitJobFixture(t, filepath.Join(root, "leaf-*.json"))
	defer grandchild.Close()
	core := start("leaf")
	defer core.Close()
	var contained int32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	for _, child := range []*JobChild{supervisor, &grandchild, core} {
		ok, _, err := proc.Call(uintptr(child.Handle), uintptr(job.handle), uintptr(unsafe.Pointer(&contained)))
		if ok == 0 || contained == 0 {
			t.Fatalf("child is outside exact Job: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := core.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	replacement := start("leaf")
	defer replacement.Close()
	for _, child := range []*JobChild{supervisor, &grandchild} {
		if alive, err := child.Alive(); err != nil || !alive {
			t.Fatalf("Core-only replacement killed Tunnel: %v", err)
		}
	}
	if replacement.PID == core.PID && replacement.Created == core.Created {
		t.Fatal("Core not replaced")
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	for _, child := range []*JobChild{supervisor, &grandchild, replacement} {
		if _, err := child.Wait(ctx); err != nil {
			t.Fatal("Job close left child alive", err)
		}
	}
	if _, err := job.Start(os.Args[0], nil, root, os.Environ(), null, null, null); err == nil {
		t.Fatal("closed Job launched a process")
	}
}
func TestCreateTimeJobForcedHostDeathLeavesNoDescendants(t *testing.T) {
	root := t.TempDir()
	host := exec.Command(os.Args[0], "-test.run=^TestCreateTimeJobHelperProcess$")
	host.Env = jobFixtureEnv("host", root)
	Configure(host)
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Process.Kill(); _ = host.Wait() }()
	child := waitJobFixture(t, filepath.Join(root, "child.json"))
	defer child.Close()
	grandchild := waitJobFixture(t, filepath.Join(root, "leaf-*.json"))
	defer grandchild.Close()
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, owned := range []*JobChild{&child, &grandchild} {
		if _, err := owned.Wait(ctx); err != nil {
			t.Fatal("forced host death orphaned a child", err)
		}
	}
}
func TestCreateTimeJobRejectsBeforeProcessCreation(t *testing.T) {
	job, err := NewJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	for _, test := range []struct {
		binary, dir string
		args, env   []string
	}{{"relative.exe", t.TempDir(), nil, nil}, {os.Args[0], "relative", nil, nil}, {os.Args[0], t.TempDir(), []string{"bad\x00arg"}, nil}, {os.Args[0], t.TempDir(), nil, []string{"BAD=\x00"}}} {
		if child, err := job.Start(test.binary, test.args, test.dir, test.env, null, null, null); err == nil || child != nil {
			t.Fatal("invalid start created a child")
		}
	}
}
