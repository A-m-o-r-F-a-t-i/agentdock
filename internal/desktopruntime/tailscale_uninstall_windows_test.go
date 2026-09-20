//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTailscaleUninstallOwnedAndForeignMappings(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "foreign"}[foreign], func(t *testing.T) {
			runtime, system := newPublicAccessTestSystem(t, "none")
			if err := system.configure(runtime, true); err != nil {
				t.Fatal(err)
			}
			hostPort := system.fake.node.DNSName + ":443"
			system.fake.config.Web[hostPort].Handlers["/other"] = &tailscaleHTTPHandler{Proxy: "http://127.0.0.1:9999"}
			if foreign {
				system.fake.config.Web[hostPort].Handlers["/"] = &tailscaleHTTPHandler{Proxy: "http://127.0.0.1:9000"}
			}
			before := system.fake.writeCount
			handled, warning, err := cleanupTailscaleFunnel(context.Background(), runtime.root, system.hooks)
			if err != nil || !handled || (warning != "") != foreign {
				t.Fatalf("cleanup=%v %q %v", handled, warning, err)
			}
			if !system.fake.config.handler(hostPort, "/other").isProxy("http://127.0.0.1:9999") {
				t.Fatal("foreign sibling was removed")
			}
			if foreign && (system.fake.writeCount != before || !system.fake.config.handler(hostPort, "/").isProxy("http://127.0.0.1:9000")) {
				t.Fatal("foreign root was changed")
			}
			if !foreign && system.fake.config.handler(hostPort, "/") != nil {
				t.Fatal("owned root remained active")
			}
			if _, err := os.Stat(filepath.Join(runtime.root, tailscaleStateFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed uninstall retained ownership", err)
			}
		})
	}
}

func TestTailscaleUninstallKeepsRecoveryAfterPostWriteIdentityChange(t *testing.T) {
	runtime, system := newPublicAccessTestSystem(t, "none")
	if err := system.configure(runtime, true); err != nil {
		t.Fatal(err)
	}
	system.fake.afterWrite = func(fake *memoryTailscaleCLI) { fake.node.ID = "changed-device" }
	handled, warning, err := cleanupTailscaleFunnel(context.Background(), runtime.root, system.hooks)
	if !handled || err == nil || warning != "" {
		t.Fatalf("uncertain write accepted: %v %q %v", handled, warning, err)
	}
	state, stateErr := loadTailscaleState(runtime.root)
	if stateErr != nil || state == nil || !state.Pending {
		t.Fatalf("recovery state lost: %+v %v", state, stateErr)
	}
}
