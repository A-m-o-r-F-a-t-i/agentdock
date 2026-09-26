package installer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/uvwt/agentdock/internal/permission"
)

// provenFreshPermissionHome runs under the installation transaction lock, before
// staging publishes any files. Missing policy.json alone is never clean-install
// evidence: an existing Home, manifest, binary, generation or transaction rejects
// the default. Setup's provisional credentials are not installation evidence.
func provenFreshPermissionHome(request Request) string {
	if request.Action != ActionInstall {
		return ""
	}
	home := skillStorageHome(request)
	if !filepath.IsAbs(home) {
		return ""
	}
	home = filepath.Clean(home)
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		return ""
	}
	for _, root := range []string{request.InstallRoot, request.RuntimeRoot} {
		for _, relative := range []string{
			"runtime.json", "desktop-runtime.json", "desktop-version.txt",
			"agentdock.env", "active-version.json", "versions", "generations",
			"install/transaction.json", "install/result.json", "install/results",
			"update/transaction.json", "agentdock", "agentdock.exe",
			"bin/agentdock", "bin/agentdock.exe", "bin/agentdock-core.exe",
		} {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
				return ""
			}
		}
	}
	return home
}

// initializeInstallPermissions exclusively claims the still-absent Home. If
// another installer/runtime created it after the probe, its data wins and no
// policy is written. The new Home is journal-owned, so a failed activation also
// rolls back the default instead of leaving full permission on a failed install.
func initializeInstallPermissions(ctx context.Context, request Request, home string, journal *rollbackJournal) error {
	if home == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if journal == nil {
		return fmt.Errorf("fresh permission initialization requires a rollback journal")
	}
	if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	if err := journal.NoteCreated(home); err != nil {
		// Remove only the empty directory we just claimed; never recurse here.
		_ = os.Remove(home)
		return err
	}
	root := filepath.Join(home, "execution", "permissions")
	if _, _, err := permission.InitializeFreshInstall(ctx, root); err != nil {
		return fmt.Errorf("initialize clean-install permission policy: %w", err)
	}
	return ownFreshPermissionHome(request, home)
}

// Only the newly claimed Home is involved, never an existing policy tree or the
// installer journal. Root-run Linux installation must leave the policy writable
// by the runtime's service user, just like the existing Skill bootstrap does.
func ownFreshPermissionHome(request Request, home string) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || strings.TrimSpace(request.ServiceUser) == "" {
		return nil
	}
	account, err := user.Lookup(request.ServiceUser)
	if err != nil {
		return err
	}
	uid, err := parseOwnershipID(account.Uid)
	if err != nil {
		return err
	}
	gid, err := parseOwnershipID(account.Gid)
	if err != nil {
		return err
	}
	entries := 0
	return filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		entries++
		if entries > 32 || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected content in newly created permission Home")
		}
		return os.Lchown(path, uid, gid)
	})
}
