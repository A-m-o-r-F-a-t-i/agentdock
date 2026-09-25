// Package bundledrg verifies the pinned Windows search component. Generation
// installation and process supervision remain owned by their existing engines.
package bundledrg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const RelativeDir = "share/agentdock/bin"

//go:embed windows-amd64.json
var specification []byte

var ErrIntegrity = errors.New("bundled ripgrep integrity check failed")

type FileSpec struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Spec struct {
	SchemaVersion int        `json:"schema_version"`
	Version       string     `json:"version"`
	Platform      string     `json:"platform"`
	Asset         string     `json:"asset"`
	URL           string     `json:"url"`
	ArchiveRoot   string     `json:"archive_root"`
	ArchiveSize   int64      `json:"archive_size"`
	ArchiveSHA256 string     `json:"archive_sha256"`
	License       string     `json:"license"`
	Files         []FileSpec `json:"files"`
}

// Specification returns a fresh value; callers cannot change the compiled pins.
func Specification() Spec {
	var spec Spec
	if err := json.Unmarshal(specification, &spec); err != nil {
		panic(err)
	}
	return spec
}

type Verified struct {
	Path    string
	Version string
	file    *os.File
}

// Close releases the read handle which denies binary modification on Windows.
func (verified *Verified) Close() error { return verified.file.Close() }

// Open checks the executable, architecture and accompanying licence bytes.
// Only absence of the entire component directory is a supported missing bundle.
// A present but partial, redirected or corrupt bundle is a fail-closed error.
func Open(ctx context.Context, root string) (*Verified, error) {
	return openBundle(ctx, root, true)
}

// OpenLegacy reads only the pinned sidecar of the same running executable.
// Earlier packages did not include a separate manifest; no active pointer or
// system-wide tool directory is consulted here.
func OpenLegacy(ctx context.Context, root string) (*Verified, error) {
	return openBundle(ctx, root, false)
}
func openBundle(ctx context.Context, root string, requireManifest bool) (*Verified, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: component directory is not a regular directory", ErrIntegrity)
	}
	// None of the component's relative ancestors may redirect ownership.
	levels := 1
	if requireManifest {
		levels = 2
	}
	ancestor := filepath.Dir(root)
	for i := 0; i < levels; i++ {
		info, err := os.Lstat(ancestor)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: redirected component ancestor", ErrIntegrity)
		}
		ancestor = filepath.Dir(ancestor)
	}
	if requireManifest {
		info, err := os.Lstat(filepath.Join(root, "manifest.json"))
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16*1024 {
			return nil, fmt.Errorf("%w: missing or invalid component manifest", ErrIntegrity)
		}
		file, err := openReadOnly(filepath.Join(root, "manifest.json"))
		if err != nil {
			return nil, fmt.Errorf("%w: component manifest: %v", ErrIntegrity, err)
		}
		data, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, 16*1024+1))
		file.Close()
		if err != nil || len(data) > 16*1024 {
			return nil, fmt.Errorf("%w: unreadable or oversized component manifest", ErrIntegrity)
		}
		var manifest Spec
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&manifest); err != nil {
			return nil, fmt.Errorf("%w: component manifest: %v", ErrIntegrity, err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF || !reflect.DeepEqual(manifest, Specification()) {
			return nil, fmt.Errorf("%w: component manifest differs from compiled pins", ErrIntegrity)
		}
	}
	spec := Specification()
	var executable *os.File
	ok := false
	defer func() {
		if !ok && executable != nil {
			_ = executable.Close()
		}
	}()
	for _, expected := range spec.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(root, expected.Path)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != expected.Size {
			return nil, fmt.Errorf("%w: %s is missing, non-regular or incomplete", ErrIntegrity, expected.Path)
		}
		file, err := openReadOnly(path)
		if err != nil {
			return nil, fmt.Errorf("%w: open %s: %v", ErrIntegrity, expected.Path, err)
		}
		if expected.Path == "rg.exe" {
			executable = file
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, expected.Size+1))
		if readErr == nil && expected.Path == "rg.exe" {
			readErr = checkArchitecture(file)
		}
		if expected.Path != "rg.exe" {
			_ = file.Close()
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: %s: %v", ErrIntegrity, expected.Path, readErr)
		}
		if size != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
			return nil, fmt.Errorf("%w: %s SHA-256 mismatch", ErrIntegrity, expected.Path)
		}
	}
	if executable == nil {
		return nil, fmt.Errorf("%w: compiled specification has no executable", ErrIntegrity)
	}
	ok = true
	return &Verified{Path: filepath.Join(root, "rg.exe"), Version: spec.Version, file: executable}, nil
}

func checkArchitecture(file *os.File) error {
	image, err := pe.NewFile(file)
	if err != nil {
		return fmt.Errorf("invalid PE image: %w", err)
	}
	defer image.Close()
	if image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		return errors.New("expected a Windows x64 PE image")
	}
	if _, ok := image.OptionalHeader.(*pe.OptionalHeader64); !ok {
		return errors.New("expected a 64-bit PE optional header")
	}
	return nil
}

type contextReader struct {
	context context.Context
	reader  io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.context.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

// VerifyIfPresent validates before/after existing generation-copy operations.
// Legacy packages without this component remain readable; new x64 packaging
// requires every pinned file and separately verifies the installed selection.
func VerifyIfPresent(ctx context.Context, base string) (bool, error) {
	root := filepath.Join(base, filepath.FromSlash(RelativeDir))
	verified, err := Open(ctx, root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, verified.Close()
}

// ArchiveFile recognizes only exact, pinned component files, never arbitrary
// tools or traversal paths. The caller retains archive ownership and limits.
func ArchiveFile(name string) bool {
	if name == RelativeDir+"/manifest.json" {
		return true
	}
	for _, file := range Specification().Files {
		if name == RelativeDir+"/"+file.Path {
			return true
		}
	}
	return false
}

func Supported(goos, arch string) bool {
	return strings.Join([]string{goos, arch}, "/") == Specification().Platform
}

// ManifestBytes returns a private copy of the pinned, human-readable source,
// version, architecture, license and SHA-256 manifest carried in the payload.
func ManifestBytes() []byte { return append([]byte(nil), specification...) }
