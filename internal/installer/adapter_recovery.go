package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/updateengine"
)

// restoreFiles does not restart services or declare rollback complete. The OS
// adapter still owns the original registry, credentials and runtime manifest.
// Same-version repairs must restore generation bytes before it starts Core.
func (engine Engine) restoreFiles(ctx context.Context, store *Store, request Request) (Result, error) {
	if strings.TrimSpace(request.TransactionID) == "" {
		return Result{}, errors.New("restore-files requires an explicit transaction-id")
	}
	transaction, current, err := bindInstallTransaction(store, request)
	if err != nil {
		return current, err
	}
	if transaction.Action == ActionUninstall {
		return current, errors.New("restore-files does not apply to uninstall transactions")
	}
	journal, err := loadJournal(store.Root(), transaction.TransactionID)
	if err != nil {
		return current, err
	}
	if journal == nil {
		if transaction.State == updateengine.StateRolledBack {
			return current, nil
		}
		return current, errors.New("restore-files: recovery journal is unavailable")
	}
	transaction.State, transaction.Phase = updateengine.StateRollingBack, PhaseRollback
	transaction.CompletedAt = nil
	if err := store.WriteTransaction(transaction); err != nil {
		return current, err
	}
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	if err := journal.restore(recovery, request, false); err != nil {
		current.Failure = &updateengine.Failure{Code: FailureRollbackFailed, Message: err.Error(), At: time.Now().UTC()}
		failed, writeErr := store.Complete(transaction, updateengine.StateFailed, current)
		return failed, errors.Join(err, writeErr)
	}
	current = projectRestoredResult(current, transaction, request, true)
	return leaveAdapterRollbackPending(store, transaction, current, nil)
}

func leaveAdapterRollbackPending(store *Store, transaction Transaction, result Result, original error) (Result, error) {
	transaction.State, transaction.Phase = updateengine.StateRollingBack, PhaseRollback
	transaction.CompletedAt = nil
	transaction.Failure = result.Failure
	transaction.ActiveVersion = result.ActiveVersion
	transaction.FallbackVersion = result.FallbackVersion
	result.State, result.Phase = updateengine.StateRollingBack, PhaseRollback
	result.CompletedAt = time.Time{}
	result.Healthy = false
	if err := store.WriteTransaction(transaction); err != nil {
		return result, errors.Join(original, err)
	}
	if err := store.WriteResult(result); err != nil {
		return result, errors.Join(original, err)
	}
	return result, original
}

func verifyTransactionHealth(ctx context.Context, transaction Transaction, request Request, expected string) error {
	if transaction.Action == ActionUninstall {
		return errors.New("uninstall cannot require a running health endpoint")
	}
	if strings.TrimSpace(expected) == "" || expected == "unknown" {
		return errors.New("cannot verify runtime health without its expected version")
	}
	probe := request
	probe.Host, probe.Port = "", 0
	host, port := resolveListenAddress(probe)
	if port == 0 {
		return errors.New("restored runtime has no health port")
	}
	endpoint := healthURL(host, port)
	if err := updateengine.WaitForVersion(ctx, []string{endpoint}, strings.TrimPrefix(expected, "v"), 45*time.Second); err != nil {
		return fmt.Errorf("runtime health verification failed before ending transaction: %w", err)
	}
	return waitHealthy(ctx, endpoint, 5*time.Second)
}
