package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/uvwt/agentdock/internal/bundledrg"
)

type rgSelection struct {
	path     string
	source   string
	version  string
	verified *bundledrg.Verified
}

func (selection rgSelection) close() {
	if selection.verified != nil {
		_ = selection.verified.Close()
	}
}
func (selection rgSelection) annotate(result Result) Result {
	result["engine_source"] = selection.source
	result["engine_path"] = selection.path
	if selection.version != "" {
		result["engine_version"] = selection.version
	}
	return result
}

func selectRG(ctx context.Context) (rgSelection, error) {
	executable, err := os.Executable()
	if err != nil {
		return rgSelection{}, err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return selectRGForExecutable(ctx, executable, runtime.GOOS, runtime.GOARCH, exec.LookPath)
}

// The actual running binary chooses its sidecar, not the mutable active pointer,
// current working directory, installed developer cache or a shell environment.
func selectRGForExecutable(ctx context.Context, executable, goos, arch string, lookPath func(string) (string, error)) (rgSelection, error) {
	if err := ctx.Err(); err != nil {
		return rgSelection{}, err
	}
	if bundledrg.Supported(goos, arch) {
		root := filepath.Join(filepath.Dir(executable), filepath.FromSlash(bundledrg.RelativeDir))
		verified, err := bundledrg.Open(ctx, root)
		if err == nil {
			return rgSelection{path: verified.Path, source: "bundled", version: verified.Version, verified: verified}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return rgSelection{}, err
		}
		// Compatibility is restricted to this executable's own legacy sidecar.
		// A present corrupt canonical bundle never downgrades to this path.
		legacy := filepath.Join(filepath.Dir(executable), "tools", "rg")
		verified, err = bundledrg.OpenLegacy(ctx, legacy)
		if err == nil {
			return rgSelection{path: verified.Path, source: "compatibility_bundled", version: verified.Version, verified: verified}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return rgSelection{}, err
		}
	}
	path, err := lookPath("rg")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrDot) {
			return rgSelection{}, nil
		}
		return rgSelection{}, err
	}
	return rgSelection{path: path, source: "path"}, nil
}
