package bundledrg

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecificationPinsOfficialX64ArtifactAndLicences(t *testing.T) {
	spec := Specification()
	if spec.SchemaVersion != 1 || spec.Version != "15.2.0" || spec.Platform != "windows/amd64" || len(spec.Files) != 4 || spec.ArchiveSize < 1 {
		t.Fatalf("invalid compiled specification: %+v", spec)
	}
	if !strings.HasPrefix(spec.URL, "https://github.com/BurntSushi/ripgrep/releases/download/"+spec.Version+"/") || strings.Contains(spec.URL, "latest") {
		t.Fatal("unverified release channel")
	}
	seen := map[string]bool{}
	for _, file := range spec.Files {
		decoded, err := hex.DecodeString(file.SHA256)
		if err != nil || len(decoded) != 32 || file.Size < 1 || seen[file.Path] || filepath.Base(file.Path) != file.Path {
			t.Fatalf("invalid file pin: %+v", file)
		}
		seen[file.Path] = true
		if !ArchiveFile(RelativeDir + "/" + file.Path) {
			t.Fatal("pinned archive member not recognized")
		}
	}
	for _, path := range []string{"share/agentdock/bin/../rg.exe", "TOOLS/rg/rg.exe", "share/agentdock/bin/other.exe", "share/agentdock/bin/rg.exe/extra", "tools\\rg\\rg.exe"} {
		if ArchiveFile(path) {
			t.Fatalf("unsafe archive member accepted: %s", path)
		}
	}
	spec.Files[0].SHA256 = "changed"
	if Specification().Files[0].SHA256 == "changed" {
		t.Fatal("caller mutated compiled trust pins")
	}
	if !Supported("windows", "amd64") || Supported("windows", "arm64") || Supported("linux", "amd64") {
		t.Fatal("unsupported platform claimed")
	}
}

func TestMissingAndCorruptBundleAreDifferentOutcomes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "share", "agentdock", "bin")
	if _, err := Open(context.Background(), root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bundle: %v", err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root); !errors.Is(err, ErrIntegrity) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial bundle must fail closed: %v", err)
	}
	binary := filepath.Join(root, "rg.exe")
	if err := os.WriteFile(binary, []byte("not a PE"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("corrupt bundle: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification: %v", err)
	}
}
