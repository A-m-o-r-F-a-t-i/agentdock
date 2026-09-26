//go:build windows

package selfupdate

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsReleasePayloadExtractsCoreSkillsAndDesktopInOnePass(t *testing.T) {
	payload, err := extractWindowsReleasePayload(makeWindowsReleaseZIP(t, nil), t.TempDir(), "agentdock.exe", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	assertReleaseFile(t, payload.CorePath, "core")
	assertReleaseFile(t, filepath.Join(payload.BundlePath, "manifest.json"), `{}`)
	assertReleaseFile(t, filepath.Join(payload.DesktopPath, "agentdock-tray.exe"), "tray")
	assertReleaseFile(t, filepath.Join(payload.DesktopPath, windowsDesktopVersionFile), "v1.2.3\n")
}

func TestWindowsReleasePayloadRejectsUnsafeCatalogue(t *testing.T) {
	tests := []struct {
		name string
		add  func(*zip.Writer)
		want string
	}{
		{
			name: "traversal",
			want: "路径",
			add: func(writer *zip.Writer) {
				writeReleaseEntry(t, writer, "../escape.exe", "escape")
			},
		},
		{
			name: "case folded duplicate",
			want: "重复条目",
			add: func(writer *zip.Writer) {
				writeReleaseEntry(t, writer, "AGENTDOCK.EXE", "duplicate")
			},
		},
		{
			name: "symlink",
			want: "不是普通文件",
			add: func(writer *zip.Writer) {
				header := &zip.FileHeader{Name: "unused-link"}
				header.SetMode(os.ModeSymlink | 0o777)
				entry, err := writer.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("agentdock.exe")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := extractWindowsReleasePayload(makeWindowsReleaseZIP(t, test.add), filepath.Join(root, "stage"), "agentdock.exe", "v1.2.3")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
			if _, statErr := os.Stat(filepath.Join(root, "escape.exe")); !os.IsNotExist(statErr) {
				t.Fatalf("unexpected traversal output: %v", statErr)
			}
		})
	}
}

func makeWindowsReleaseZIP(t *testing.T, add func(*zip.Writer)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := [][2]string{
		{"agentdock.exe", "core"},
		{coreSkillBundlePrefix + "manifest.json", `{}`},
		{coreSkillBundlePrefix + "README.md", "skill"},
		{"agentdock-tray.exe", "tray"},
		{"agentdock.ico", "icon"},
		{"agentdock-arbiter.exe", "arbiter"},
		{"agentdock-shim.exe", "shim"},
		{"agentdock-tray-shim.exe", "tray-shim"},
		{"wsl-helper/manifest.json", `{}`},
		{"wsl-helper/agentdock-wsl-helper-linux-amd64", "amd64"},
		{"wsl-helper/agentdock-wsl-helper-linux-arm64", "arm64"},
	}
	for _, file := range files {
		writeReleaseEntry(t, writer, file[0], file[1])
	}
	if add != nil {
		add(writer)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeReleaseEntry(t *testing.T, writer *zip.Writer, name, content string) {
	t.Helper()
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

func assertReleaseFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("%s=%q, want %q", path, data, expected)
	}
}
