package permission

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// FreshInstallPolicy is the single source of truth for a proven clean install.
// Custom settings start disabled, so Full mode controls admission while explicit
// deny rules retain their normal precedence. Installers must not use this as an
// upgrade heuristic merely because policy.json is absent.
func FreshInstallPolicy() Policy {
	disabled := false
	settings := DefaultSettings()
	return Policy{
		Settings:                 &settings,
		CustomPermissionsEnabled: &disabled,
		SchemaVersion:            CurrentSchemaVersion,
		Revision:                 1,
		GlobalMode:               Full,
		Scopes:                   []Scope{},
		Rules:                    []Rule{},
		UpdatedAt:                time.Now().UTC(),
	}
}

// InitializeFreshInstall atomically creates the clean-install policy only when
// no durable policy exists. Existing policy bytes and user/admin choices are
// preserved. The caller owns proving that the surrounding installation is new.
func InitializeFreshInstall(ctx context.Context, root string) (policy Policy, created bool, err error) {
	store, err := New(root)
	if err != nil {
		return Policy{}, false, err
	}
	err = store.locked(ctx, func() error {
		path := filepath.Join(store.root, "policy.json")
		if _, statErr := os.Lstat(path); statErr == nil {
			policy, err = store.loadPolicy()
			return err
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		policy = FreshInstallPolicy()
		if err = validatePolicy(policy); err != nil {
			return err
		}
		if err = writeJSON(ctx, path, policy); err != nil {
			return err
		}
		created = true
		return nil
	})
	return policy, created, err
}
