package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/uvwt/agentdock/internal/desktopruntime"
	"github.com/uvwt/agentdock/internal/fs/filelock"
	"github.com/uvwt/agentdock/internal/plugin"
)

// MigratePluginHome is an explicit offline operation. Its restartable journal
// also makes the command safe to retry after interruption; normal startup never
// changes portable packages. Windows Setup uses its larger install journal.
func MigratePluginHome(ctx context.Context, home string) ([]string, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("plugin migration home must be absolute")
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		return nil, errors.New("plugin migration home must exist")
	}
	release, err := filelock.Acquire(ctx, filepath.Join(home, "plugins", ".locks", "store.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	const migrationID = "agent-plugins-1"
	journal, err := loadJournal(home, migrationID)
	if err != nil {
		return nil, err
	}
	if journal != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), installRecoveryTimeout)
		err := journal.Restore(recovery, Request{})
		cancel()
		if err != nil {
			return nil, err
		}
		discardJournal(home, migrationID)
	}
	plan, err := plugin.PlanLegacyMigration(home)
	if err != nil {
		return nil, err
	}
	if len(plan.Names()) == 0 {
		return []string{}, nil
	}
	journal = newJournal(home, migrationID)
	if err := plan.Apply(ctx, journal.Snapshot); err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), installRecoveryTimeout)
		restoreErr := journal.Restore(recovery, Request{})
		cancel()
		if restoreErr == nil {
			discardJournal(home, migrationID)
		}
		return nil, errors.Join(err, restoreErr)
	}
	discardJournal(home, migrationID)
	return plan.Names(), nil
}

func migrateInstalledPlugins(ctx context.Context, request Request, journal *rollbackJournal) error {
	// Desktop packages are currently Windows-only. Other platforms use the
	// explicit offline command under the owning service account.
	if runtimeGOOS() != "windows" {
		return nil
	}
	home := request.AgentDockHome
	if home == "" {
		manifest, err := desktopruntime.Load(filepath.Join(request.RuntimeRoot, "runtime.json"))
		if err != nil {
			return err
		}
		home = manifest.AgentDockHome
	}
	if home == "" {
		return nil
	}
	plan, err := plugin.PlanLegacyMigration(home)
	if err != nil {
		return err
	}
	if len(plan.Names()) == 0 {
		return nil
	}
	for _, service := range journal.Services {
		if service.Manager == "windows" && service.Name == "agentdock" {
			if err := stopJournalService(ctx, request, service); err != nil {
				return err
			}
		}
	}
	return plan.Apply(ctx, journal.Snapshot)
}
