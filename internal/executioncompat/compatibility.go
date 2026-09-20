// Package executioncompat preserves execution-policy intent across managed
// runtime changes. It reads metadata only and never rewrites policy or history.
package executioncompat

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const PolicyVersion = 1

var ErrUnsupported = errors.New("EXECUTION_POLICY_DOWNGRADE_BLOCKED: target runtime cannot enforce the saved execution policy; keep the current runtime or restore a policy-capable version")

// RequiredVersion does not infer compatibility from a product version string.
// A policy file or any approval record is sufficient evidence of saved intent.
// Invalid/unreadable metadata fails closed without changing the original bytes.
func RequiredVersion(home string) (int, error) {
	if !filepath.IsAbs(home) {
		return 0, errors.New("execution-policy check requires an absolute data directory")
	}
	root := filepath.Join(home, "execution", "permissions")
	for _, dir := range []string{home, filepath.Dir(root), root} {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("execution-policy directories must be real directories")
		}
	}
	if info, err := os.Lstat(filepath.Join(root, "policy.json")); err == nil {
		if !info.Mode().IsRegular() {
			return 0, errors.New("execution-policy record must be a regular file")
		}
		return PolicyVersion, nil
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	dir := filepath.Join(root, "approvals")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, errors.New("approval storage must be a real directory")
	}
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(32)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				return PolicyVersion, nil
			}
		}
		if err == io.EOF {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

func Validate(required, supported int) error {
	if required > supported {
		return ErrUnsupported
	}
	return nil
}
