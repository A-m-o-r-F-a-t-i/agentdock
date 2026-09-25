//go:build windows

package desktopruntime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	processctl "github.com/uvwt/agentdock/internal/process"
	"golang.org/x/sys/windows"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRuntimeHostProtocolIdentityAndBounds(t *testing.T) {
	valid := RuntimeHostRequest{SchemaVersion: 1, Op: RuntimeHostReplaceOrigin, RequestID: 1, Epoch: strings.Repeat("a", 32), Generation: "v1.1.6", Origin: "https://valid-name.trycloudflare.com"}
	var out bytes.Buffer
	if err := WriteRuntimeHostFrame(&out, valid); err != nil {
		t.Fatal(err)
	}
	var decoded RuntimeHostRequest
	if err := ReadRuntimeHostFrame(bufio.NewReaderSize(&out, RuntimeHostFrameLimit+1), &decoded); err != nil || decoded != valid {
		t.Fatal(decoded, err)
	}
	for _, origin := range []string{"https://bad.trycloudflare.com/path", "http://valid.trycloudflare.com", "https://valid.trycloudflare.com:443", "https://u@valid.trycloudflare.com", "https://valid.trycloudflare.com?", "https://.trycloudflare.com", "https://-bad.trycloudflare.com", "https://bad-.trycloudflare.com", "https://" + strings.Repeat("a", 64) + ".trycloudflare.com"} {
		bad := valid
		bad.Origin = origin
		if bad.Validate(valid.Epoch, valid.Generation, 1) == nil {
			t.Fatal("invalid origin accepted", origin)
		}
	}
	for _, change := range []func(*RuntimeHostRequest){func(v *RuntimeHostRequest) { v.RequestID = 2 }, func(v *RuntimeHostRequest) { v.Op = "exec" }, func(v *RuntimeHostRequest) { v.SchemaVersion = 2 }, func(v *RuntimeHostRequest) { v.Epoch = "old" }, func(v *RuntimeHostRequest) { v.Generation = "v1.1.5" }} {
		bad := valid
		change(&bad)
		if bad.Validate(valid.Epoch, valid.Generation, 1) == nil {
			t.Fatal("stale/unknown request accepted")
		}
	}
	if valid.Validate(valid.Epoch, valid.Generation, 2) == nil {
		t.Fatal("duplicate id accepted")
	}
	valid.Origin = ""
	if err := valid.Validate(valid.Epoch, valid.Generation, 1); err != nil {
		t.Fatal("origin invalidation rejected", err)
	}
	for _, frame := range []string{"{} {}\n", "{\"unknown\":true}\n", "{\"origin\":\"\xff\"}\n", strings.Repeat("a", RuntimeHostFrameLimit+1) + "\n", "{\"schema_version\":"} {
		if err := ReadRuntimeHostFrame(bufio.NewReaderSize(strings.NewReader(frame), RuntimeHostFrameLimit+1), &decoded); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}
func TestRuntimeTaskOwnerContractAndOrderedActions(t *testing.T) {
	root := t.TempDir()
	sid := "S-1-5-21-123-456-789-1001"
	good := runtimeTaskContract{Name: "AgentDock", UserSID: sid, Path: filepath.Join(root, "bin", "agentdock-tray.exe"), Arguments: `--run-core-task --runtime-root "` + root + `"`, LogonType: taskLogonInteractiveToken, RunLevel: 1, Actions: 1, ActionType: 0}
	if err := validateRuntimeTaskContract(root, "AgentDock", sid, good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*runtimeTaskContract){func(v *runtimeTaskContract) { v.Name = "other" }, func(v *runtimeTaskContract) { v.UserSID = "S-1-5-18" }, func(v *runtimeTaskContract) { v.Path = filepath.Join(root, "other.exe") }, func(v *runtimeTaskContract) { v.Arguments += " --extra" }, func(v *runtimeTaskContract) { v.RunLevel = 0 }, func(v *runtimeTaskContract) { v.LogonType = 1 }, func(v *runtimeTaskContract) { v.Actions = 2 }, func(v *runtimeTaskContract) { v.WorkingDirectory = filepath.Dir(root) }} {
		bad := good
		change(&bad)
		if validateRuntimeTaskContract(root, "AgentDock", sid, bad) == nil {
			t.Fatal("foreign task accepted", bad)
		}
	}
	for _, action := range []string{"start", "stop", "restart", "regenerate"} {
		for _, failAt := range []string{"", "validate", "end", "run"} {
			var calls []string
			call := func(name string) func(context.Context) error {
				return func(ctx context.Context) error {
					calls = append(calls, name)
					if name == failAt {
						return errors.New(name)
					}
					return nil
				}
			}
			err := applyScheduledAction(t.Context(), action, scheduledActionOps{Validate: call("validate"), End: call("end"), Run: call("run")})
			expected := []string{"validate"}
			if failAt != "validate" {
				if action != "start" {
					expected = append(expected, "end")
				}
				if action != "stop" && (action == "start" || failAt != "end") {
					expected = append(expected, "run")
				}
			}
			if !reflect.DeepEqual(calls, expected) {
				t.Fatalf("%s/%s: %v want %v", action, failAt, calls, expected)
			}
			if failAt != "" && calls[len(calls)-1] == failAt && err == nil {
				t.Fatal("failed action became success")
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := applyScheduledAction(ctx, "restart", scheduledActionOps{Validate: func(context.Context) error { t.Fatal("cancelled request mutated task"); return nil }}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRuntimeTaskStartWithCOMWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	sid := "S-1-5-21-123-456-789-1001"
	for _, test := range []struct {
		name      string
		value     func() variant
		wantStart bool
	}{
		// TaskAdmin leaves this optional property unset. Task Scheduler can
		// return a null BSTR, which must behave like an allocated empty BSTR.
		{"unset", func() variant { return variant{VT: vtBstr} }, true},
		{"empty", func() variant { return variantBSTR("") }, true},
		{"owned-root", func() variant { return variantBSTR(root) }, true},
		{"foreign-root", func() variant { return variantBSTR(filepath.Dir(root)) }, false},
		{"integer-zero", func() variant { return variantInt32(0) }, false},
		{"missing-value", variantEmpty, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract := runtimeTaskContract{
				Name: "AgentDock", UserSID: sid,
				Path:             filepath.Join(root, "bin", "agentdock-tray.exe"),
				Arguments:        `--run-core-task --runtime-root "` + root + `"`,
				WorkingDirectory: variantString(test.value()),
				LogonType:        taskLogonInteractiveToken, RunLevel: 1, Actions: 1, ActionType: 0,
			}
			started := false
			err := applyScheduledAction(t.Context(), "start", scheduledActionOps{
				Validate: func(context.Context) error {
					return validateRuntimeTaskContract(root, "AgentDock", sid, contract)
				},
				Run: func(context.Context) error { started = true; return nil },
			})
			if started != test.wantStart || (err == nil) != test.wantStart {
				t.Fatalf("working directory=%q: started=%t, want %t; err=%v", contract.WorkingDirectory, started, test.wantStart, err)
			}
		})
	}
}

func TestRuntimeCapturedHealthRejectsWrongPIDVersionOriginAndCancelledWait(t *testing.T) {
	identity, err := currentRuntimeHostIdentity()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	child := &processctl.JobChild{Handle: handle, PID: identity.PID, Created: identity.Created}
	defer child.Close()
	for _, scenario := range []string{"good", "wrong-pid", "wrong-version", "wrong-origin", "false-ok", "invalid", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "redirect" {
					http.Redirect(w, r, "https://outside.invalid", http.StatusFound)
					return
				}
				if scenario == "invalid" {
					fmt.Fprint(w, "{}")
					return
				}
				pid := identity.PID
				version := "1.1.6"
				hash := runtimeOriginDigest("")
				ok := true
				switch scenario {
				case "wrong-pid":
					pid++
				case "wrong-version":
					version = "1.1.5"
				case "wrong-origin":
					hash = "stale"
				case "false-ok":
					ok = false
				}
				fmt.Fprintf(w, `{"ok":%t,"process_id":%d,"version":%q,"origin_hash":%q}`, ok, pid, version, hash)
			}))
			defer server.Close()
			var port int
			fmt.Sscanf(strings.TrimPrefix(server.URL, "http://127.0.0.1:"), "%d", &port)
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			err := waitCapturedCoreHealth(ctx, Manifest{Host: "127.0.0.1", Port: port}, child, "v1.1.6", runtimeOriginDigest(""))
			if (err == nil) != (scenario == "good") {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
		})
	}
	if _, err := readRuntimeCoreHealth(t.Context(), "http://example.com/healthz"); err == nil {
		t.Fatal("nonloopback accepted")
	}
	stale := identity
	stale.Created++
	if reopened, err := openRuntimeHostProcess(stale, os.Args[0]); err != nil || reopened != nil {
		t.Fatal("reused PID accepted", err)
	}
}
