//go:build windows && amd64 && bundled_rg_integration

package bundledrg

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func realBundleCopy(t *testing.T) string {
	t.Helper()
	source := os.Getenv("AGENTDOCK_TEST_RG_BUNDLE")
	if source == "" {
		t.Fatal("required real-bundle fixture is missing; this suite never skips")
	}
	root := filepath.Join(t.TempDir(), "share", "agentdock", "bin")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, expected := range Specification().Files {
		data, err := os.ReadFile(filepath.Join(source, expected.Path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, expected.Path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), ManifestBytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRequiredRealBundleIsVerifiedAndHeldReadOnly(t *testing.T) {
	root := realBundleCopy(t)
	verified, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	output, err := exec.Command(verified.Path, "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(output), "ripgrep "+verified.Version) {
		t.Fatalf("actual binary: %s, %v", output, err)
	}
	file, err := os.OpenFile(verified.Path, os.O_WRONLY, 0)
	if err == nil {
		file.Close()
		t.Fatal("verified executable could be opened for modification before search completed")
	}
	if err := os.Rename(verified.Path, verified.Path+".changed"); err == nil {
		t.Fatal("verified executable could be replaced before search completed")
	}
}

func TestRequiredRealBundleRejectsMutations(t *testing.T) {
	for _, scenario := range []string{"same-size-tamper", "wrong-arch", "partial-download", "missing-executable", "missing-licence", "modified-licence"} {
		t.Run(scenario, func(t *testing.T) {
			root := realBundleCopy(t)
			path := filepath.Join(root, "rg.exe")
			bytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "same-size-tamper":
				bytes[len(bytes)-1] ^= 1
			case "wrong-arch":
				offset := binary.LittleEndian.Uint32(bytes[0x3c:])
				binary.LittleEndian.PutUint16(bytes[offset+4:], 0xaa64)
			case "partial-download":
				bytes = bytes[:len(bytes)/2]
			case "missing-executable":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "missing-licence":
				if err := os.Remove(filepath.Join(root, "LICENSE-MIT")); err != nil {
					t.Fatal(err)
				}
			case "modified-licence":
				if err := os.WriteFile(filepath.Join(root, "COPYING"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "same-size-tamper" || scenario == "wrong-arch" || scenario == "partial-download" {
				if err := os.WriteFile(path, bytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Open(context.Background(), root); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("mutation not rejected: %v", err)
			}
		})
	}
}

func TestRequiredConcurrentBundleReadersKeepExecutableImmutable(t *testing.T) {
	root := realBundleCopy(t)
	const readers = 16
	acquired := make(chan error, readers)
	released := make(chan error, readers)
	finish := make(chan struct{})
	for index := 0; index < readers; index++ {
		go func() {
			verified, err := Open(context.Background(), root)
			acquired <- err
			if err != nil {
				released <- err
				return
			}
			<-finish
			released <- verified.Close()
		}()
	}
	var failed error
	for index := 0; index < readers; index++ {
		if err := <-acquired; err != nil {
			failed = err
		}
	}
	path := filepath.Join(root, "rg.exe")
	writable, writeErr := os.OpenFile(path, os.O_WRONLY, 0)
	if writeErr == nil {
		writable.Close()
	}
	close(finish)
	for index := 0; index < readers; index++ {
		if err := <-released; err != nil {
			failed = err
		}
	}
	if failed != nil {
		t.Fatal(failed)
	}
	if writeErr == nil {
		t.Fatal("concurrent searches did not protect the verified binary")
	}
	writable, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("completed searches leaked a read lock: %v", err)
	}
	writable.Close()
}

func TestRequiredRealBundleRejectsChangedManifest(t *testing.T) {
	root := realBundleCopy(t)
	data := strings.ReplaceAll(string(ManifestBytes()), `"15.2.0"`, `"99.0.0"`)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("changed manifest accepted: %v", err)
	}
}
