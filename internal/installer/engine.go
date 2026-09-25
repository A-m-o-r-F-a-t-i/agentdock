package installer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/fs/processlock"
	skills "github.com/uvwt/agentdock/internal/skill"
	skillbundle "github.com/uvwt/agentdock/internal/skill/bundle"
	skillstate "github.com/uvwt/agentdock/internal/skill/state"
	"github.com/uvwt/agentdock/internal/updateengine"
)

type Engine struct{}

func (engine Engine) Run(ctx context.Context, request Request) (Result, error) {
	request, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	installRoot, err := absPath(request.InstallRoot)
	if err != nil {
		return Result{}, fmt.Errorf("install-root: %w", err)
	}
	runtimeRoot, err := absPath(request.RuntimeRoot)
	if err != nil {
		return Result{}, fmt.Errorf("runtime-root: %w", err)
	}
	if request.RuntimeRootLiteral == "" {
		request.RuntimeRootLiteral = request.RuntimeRoot
	}
	request.InstallRoot = installRoot
	request.RuntimeRoot = runtimeRoot
	if request.Action == ActionInstall || request.Action == ActionRepair {
		request, err = hydrateExistingRuntime(request)
		if err != nil {
			return Result{}, err
		}
	}

	store, err := NewStore(request.StateRoot())
	if err != nil {
		return Result{}, err
	}
	lock, err := processlock.Acquire(ctx, store.LockPath())
	if err != nil {
		return Result{}, fmt.Errorf("lock install transaction: %w", err)
	}
	defer lock.Release()

	switch request.Action {
	case ActionUninstall:
		if recovered, err := engine.recoverInterrupted(ctx, store, request); err != nil {
			return recovered, err
		}
		return engine.uninstall(ctx, store, request)
	case ActionAbandon:
		return engine.abandon(ctx, store, request)
	case ActionCommit:
		return engine.commit(ctx, store, request)
	case ActionRestoreFiles:
		return engine.restoreFiles(ctx, store, request)
	case ActionRepair:
		request.Channel = "repair"
		if recovered, err := engine.recoverInterrupted(ctx, store, request); err != nil {
			return recovered, err
		}
		return engine.install(ctx, store, request)
	default:
		if recovered, err := engine.recoverInterrupted(ctx, store, request); err != nil {
			return recovered, err
		}
		return engine.install(ctx, store, request)
	}
}

const installRecoveryTimeout = 3 * time.Minute

func (engine Engine) recoverInterrupted(ctx context.Context, store *Store, request Request) (Result, error) {
	transaction, err := store.ReadTransaction()
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, nil
		}
		return Result{}, err
	}

	// 卸载事务没有 install rollback journal，不能按安装中断去 Restore。
	if transaction.Action == ActionUninstall {
		return Result{}, nil
	}

	// 终态事务以 transaction.json 为准，不能先读 rollback journal。
	// 成功安装后旧 journal 损坏或被清掉，都不能阻断下一次 install。
	switch transaction.State {
	case updateengine.StateCommitted:
		if err := commitWindowsActivePointer(transaction.InstallRoot, transaction.TransactionID); err != nil {
			return resultFromTransaction(transaction), err
		}
		discardJournal(store.Root(), transaction.TransactionID)
		return Result{}, nil
	case updateengine.StateRolledBack:
		if err := releaseWindowsTrialPointer(transaction.InstallRoot, transaction.TransactionID); err != nil {
			return resultFromTransaction(transaction), fmt.Errorf("previous rollback left a trial generation pointer: %w", err)
		}
		discardJournal(store.Root(), transaction.TransactionID)
		return Result{}, nil
	case "":
		return Result{}, nil
	case updateengine.StateFailed:
		if isExternalRollbackFailure(transaction) {
			if request.Action == ActionUninstall {
				return Result{}, nil
			}
			result := projectRestoredResult(resultFromTransaction(transaction), transaction, request, false)
			return result, errors.New("previous OS adapter rollback failed; repair Task/Registry/service state, then run install abandon without --rollback-failed")
		}
	}

	if transaction.State == updateengine.StateTrial {
		owned, err := windowsPointerCommittedBy(transaction.InstallRoot, transaction.TransactionID)
		if err != nil {
			return resultFromTransaction(transaction), err
		}
		if owned {
			current, readErr := store.ReadResult(transaction.TransactionID)
			if readErr != nil {
				current = resultFromTransaction(transaction)
			}
			if _, err := commitPreparedInstall(store, transaction, current); err != nil {
				return current, err
			}
			return Result{}, nil
		}
	}

	journal, err := loadJournal(store.Root(), transaction.TransactionID)
	if err != nil {
		return Result{}, fmt.Errorf("load interrupted install journal: %w", err)
	}

	if transaction.State == updateengine.StateFailed && journal == nil {
		if installPhaseMayHaveMutatedFiles(transaction.Phase) {
			return resultFromTransaction(transaction), errors.New("interrupted install has no rollback journal after files may have changed")
		}
		return Result{}, nil
	}

	result := resultFromTransaction(transaction)
	result.Failure = &updateengine.Failure{
		Code:    FailureTrialInterrupted,
		Message: "previous install trial was interrupted before commit",
		At:      time.Now().UTC(),
	}
	if journal == nil {
		result = projectRestoredResult(result, transaction, request, false)
		completed, completeErr := store.Complete(transaction, updateengine.StateFailed, result)
		if completeErr != nil {
			return result, completeErr
		}
		if !installPhaseMayHaveMutatedFiles(transaction.Phase) {
			return completed, nil
		}
		return completed, errors.New("interrupted install has no rollback journal after files may have changed")
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), installRecoveryTimeout)
	defer cancel()
	if rollbackErr := journal.Restore(rollbackCtx, request); rollbackErr != nil {
		result.Failure.Code = FailureRollbackFailed
		result.Failure.Message = errors.Join(errors.New(result.Failure.Message), rollbackErr).Error()
		result = projectRestoredResult(result, transaction, request, false)
		transaction.ActiveVersion = result.ActiveVersion
		transaction.FallbackVersion = result.FallbackVersion
		completed, completeErr := store.Complete(transaction, updateengine.StateFailed, result)
		if completeErr != nil {
			return result, errors.Join(rollbackErr, completeErr)
		}
		return completed, rollbackErr
	}
	result = projectRestoredResult(result, transaction, request, true)
	transaction.ActiveVersion = result.ActiveVersion
	transaction.FallbackVersion = result.FallbackVersion
	return sealRolledBackInstall(store, transaction, result, nil)
}

func (engine Engine) install(ctx context.Context, store *Store, request Request) (Result, error) {
	platform := currentPlatform()
	sourceVersion := existingVersion(request)
	if request.Version == "" {
		if request.Action == ActionRepair && request.PayloadDir == "" && sourceVersion != "" {
			request.Version = sourceVersion
		} else if version, err := payloadVersion(request); err == nil {
			request.Version = version
		} else {
			request.Version = "unknown"
		}
	}

	transaction, err := newTransaction(request, platform, sourceVersion)
	if err != nil {
		return Result{}, err
	}
	if request.TransactionID != "" {
		if _, statErr := os.Stat(store.ResultPath(transaction.TransactionID)); !os.IsNotExist(statErr) {
			return Result{}, errors.New("install transaction-id has already been used or cannot be inspected")
		}
	}
	timing := newInstallTimingRecorder(transaction.StartedAt)
	timing.begin(InstallStageInstallerStaging)
	transaction.Timing = timing.snapshot()
	if err := store.WriteTransaction(transaction); err != nil {
		return Result{}, err
	}
	journal := newJournal(store.Root(), transaction.TransactionID)
	result := Result{
		SchemaVersion: SchemaVersion,
		TransactionID: transaction.TransactionID,
		Platform:      platform,
		Action:        request.Action,
		Version:       request.Version,
		StartedAt:     transaction.StartedAt,
	}
	syncTiming := func() {
		summary := timing.snapshot()
		transaction.Timing = cloneTimingSummary(summary)
		result.Timing = cloneTimingSummary(summary)
	}
	timing.finishActive()
	syncTiming()

	fail := func(phase Phase, err error, staged stagedInstall) (Result, error) {
		timing.finishActive()
		syncTiming()
		transaction.Phase = PhaseRollback
		result.Failure = &updateengine.Failure{Code: string(phase) + "_failed", Message: err.Error(), At: time.Now().UTC()}
		if staged.Journal != nil {
			if rollbackErr := rollbackInstall(ctx, request, staged); rollbackErr != nil {
				result.Failure.Code = FailureRollbackFailed
				result.Failure.Message = errors.Join(err, rollbackErr).Error()
				result = projectRestoredResult(result, transaction, request, false)
				transaction.ActiveVersion = result.ActiveVersion
				transaction.FallbackVersion = result.FallbackVersion
				completed, completeErr := store.Complete(transaction, updateengine.StateFailed, result)
				if completeErr != nil {
					return result, errors.Join(err, rollbackErr, completeErr)
				}
				return completed, errors.Join(err, rollbackErr)
			}
			result = projectRestoredResult(result, transaction, request, true)
			transaction.ActiveVersion = result.ActiveVersion
			transaction.FallbackVersion = result.FallbackVersion
			if runtimeGOOS() == "windows" && request.DeferCommit {
				return leaveAdapterRollbackPending(store, transaction, result, err)
			}
			return sealRolledBackInstall(store, transaction, result, err)
		}
		transaction.Phase = phase
		result = projectRestoredResult(result, transaction, request, false)
		completed, completeErr := store.Complete(transaction, updateengine.StateFailed, result)
		if completeErr != nil {
			return result, errors.Join(err, completeErr)
		}
		return completed, err
	}

	transaction.Phase = PhaseVerify
	timing.begin(InstallStageVerify)
	syncTiming()
	if err := store.WriteTransaction(transaction); err != nil {
		return fail(PhaseVerify, err, stagedInstall{})
	}
	if request.Action == ActionRepair && request.PayloadDir == "" && request.BinaryPath == "" {
		request.BinaryPath = repairBinary(request)
	}
	if err := verifyRequest(request); err != nil {
		return fail(PhaseVerify, err, stagedInstall{})
	}
	timing.finishActive()
	syncTiming()

	transaction.Phase = PhaseStage
	timing.begin(InstallStagePayloadWrite)
	syncTiming()
	if err := store.WriteTransaction(transaction); err != nil {
		return fail(PhaseStage, err, stagedInstall{})
	}
	staged, err := stagePayload(request, journal)
	if err != nil {
		return fail(PhaseStage, err, stagedInstall{Journal: journal})
	}
	timing.finishActive()
	syncTiming()

	transaction.Phase = PhaseActivate
	transaction.State = updateengine.StateTrial
	timing.begin(InstallStageConfigSkillBootstrap)
	syncTiming()
	if err := store.WriteTransaction(transaction); err != nil {
		return fail(PhaseActivate, err, staged)
	}
	activated, err := activateInstall(ctx, request, staged)
	if err != nil {
		return fail(PhaseActivate, err, staged)
	}
	result.LocalMCPURL = activated.LocalMCPURL
	result.PublicURL = activated.PublicURL
	result.PrivilegeMode = activated.PrivilegeMode
	result.ActiveVersion = activated.ActiveVersion
	result.Warnings = append(result.Warnings, activated.Warnings...)
	transaction.ActiveVersion = activated.ActiveVersion
	if err := migrateInstalledPlugins(ctx, request, staged.Journal); err != nil {
		return fail(PhaseActivate, err, staged)
	}
	timing.finishActive()
	syncTiming()

	if request.StartService {
		transaction.Phase = PhaseStart
		timing.begin(InstallStageServiceStart)
		syncTiming()
		if err := store.WriteTransaction(transaction); err != nil {
			return fail(PhaseStart, err, staged)
		}
		if err := startPlatformServices(ctx, request, staged.Journal); err != nil {
			return fail(PhaseStart, err, staged)
		}
		timing.finishActive()
		syncTiming()

		if !request.SkipHealth && shouldWaitForHealth(request) {
			transaction.Phase = PhaseHealth
			timing.begin(InstallStageReadinessWait)
			syncTiming()
			if err := store.WriteTransaction(transaction); err != nil {
				return fail(PhaseHealth, err, staged)
			}
			host, port := resolveListenAddress(request)
			endpoint := healthURL(host, port)
			var healthErr error
			if runtimeGOOS() != "darwin" && request.Version != "unknown" {
				healthErr = updateengine.WaitForVersion(ctx, []string{endpoint}, strings.TrimPrefix(request.Version, "v"), 45*time.Second)
			}
			if healthErr != nil {
				return fail(PhaseHealth, healthErr, staged)
			}
			if waitErr := waitHealthyWithProbe(ctx, request, endpoint, 45*time.Second); waitErr != nil {
				if healthErr != nil {
					waitErr = errors.Join(healthErr, waitErr)
				}
				return fail(PhaseHealth, waitErr, staged)
			}
			result.Healthy = true
			timing.finishActive()
			syncTiming()
		}
	}

	if !request.SkipSkills && strings.TrimSpace(staged.SkillBundle) != "" {
		transaction.Phase = PhaseSkills
		timing.begin(InstallStageConfigSkillBootstrap)
		syncTiming()
		if err := store.WriteTransaction(transaction); err != nil {
			return fail(PhaseSkills, err, staged)
		}
		if err := bootstrapSkills(ctx, request, staged.LiveBinary, staged.SkillBundle); err != nil {
			return fail(PhaseSkills, err, staged)
		}
		timing.finishActive()
		syncTiming()
	}

	if shouldStartTunnelInTransaction(request) {
		transaction.Phase = PhaseTunnel
		syncTiming()
		if err := store.WriteTransaction(transaction); err != nil {
			return fail(PhaseTunnel, err, staged)
		}
		if err := startTunnelServices(ctx, request, staged.Journal); err != nil {
			result.Warnings = append(result.Warnings, "Tunnel startup could not be scheduled: "+err.Error())
		} else if request.TunnelMode == "quick" {
			if publicURL := readQuickTunnelURL(request.RuntimeRoot); publicURL != "" {
				result.PublicURL = publicURL
			}
		}
	}
	timing.complete()
	syncTiming()

	if request.DeferCommit {
		transaction.State = updateengine.StateTrial
		transaction.Phase = PhaseCommit
		if err := store.WriteTransaction(transaction); err != nil {
			return result, err
		}
		result.SchemaVersion = SchemaVersion
		result.TransactionID = transaction.TransactionID
		result.Platform = platform
		result.Action = request.Action
		result.State = updateengine.StateTrial
		result.Phase = PhaseCommit
		result.StartedAt = transaction.StartedAt
		if err := store.WriteResult(result); err != nil {
			return result, err
		}
		return result, nil
	}

	transaction.Phase = PhaseCommit
	syncTiming()
	if err := store.WriteTransaction(transaction); err != nil {
		return fail(PhaseCommit, err, staged)
	}
	completed, err := commitPreparedInstall(store, transaction, result)
	if err != nil {
		return result, err
	}

	return completed, nil
}

func shouldStartTunnelInTransaction(request Request) bool {
	if !request.StartService || request.DeferCommit {
		return false
	}
	return request.TunnelMode == "quick" || request.TunnelMode == "named"
}

func installPhaseMayHaveMutatedFiles(phase Phase) bool {
	switch phase {
	case PhasePrepare, PhaseVerify, "":
		return false
	default:
		return true
	}
}

func commitPreparedInstall(store *Store, transaction Transaction, result Result) (Result, error) {
	return commitPreparedInstallWithJournal(store, transaction, result, false)
}

func commitPreparedInstallWithJournal(store *Store, transaction Transaction, result Result, keepJournal bool) (Result, error) {
	if err := commitWindowsActivePointer(transaction.InstallRoot, transaction.TransactionID); err != nil {
		return result, err
	}
	completed, err := store.Complete(transaction, updateengine.StateCommitted, result)
	if err != nil {
		return result, err
	}
	if warnings := cleanupCommittedGenerations(transaction, completed); len(warnings) > 0 {
		updated, warningErr := store.AppendTerminalWarnings(transaction.TransactionID, warnings...)
		if warningErr != nil {
			completed.Warnings = append(completed.Warnings, "generation cleanup warning persistence failed: "+warningErr.Error())
		} else {
			completed = updated
		}
	}
	if !keepJournal {
		discardJournal(store.Root(), transaction.TransactionID)
	}
	return completed, nil
}

func sealRolledBackInstall(store *Store, transaction Transaction, result Result, original error) (Result, error) {
	if err := releaseWindowsTrialPointer(transaction.InstallRoot, transaction.TransactionID); err != nil {
		if result.Failure == nil {
			result.Failure = &updateengine.Failure{At: time.Now().UTC()}
		}
		result.Failure.Code = FailureRollbackFailed
		pointerErr := fmt.Errorf("release windows trial pointer: %w", err)
		if strings.TrimSpace(result.Failure.Message) == "" {
			result.Failure.Message = pointerErr.Error()
		} else {
			result.Failure.Message = errors.Join(errors.New(result.Failure.Message), pointerErr).Error()
		}
		transaction.Phase = PhaseRollback
		transaction.ActiveVersion = result.ActiveVersion
		transaction.FallbackVersion = result.FallbackVersion
		completed, completeErr := store.Complete(transaction, updateengine.StateFailed, result)
		if completeErr != nil {
			return result, errors.Join(original, err, completeErr)
		}
		return completed, errors.Join(original, err)
	}
	transaction.Phase = PhaseRollback
	transaction.ActiveVersion = result.ActiveVersion
	transaction.FallbackVersion = result.FallbackVersion
	completed, completeErr := store.Complete(transaction, updateengine.StateRolledBack, result)
	if completeErr != nil {
		return result, errors.Join(original, completeErr)
	}
	discardJournal(store.Root(), transaction.TransactionID)
	return completed, original
}

func removeWarning(warnings []string, remove string) []string {
	filtered := warnings[:0]
	for _, warning := range warnings {
		if warning != remove {
			filtered = append(filtered, warning)
		}
	}
	return filtered
}

func windowsUninstallAdapterWarnings(request Request) []string {
	if runtimeGOOS() != "windows" || request.PurgeData {
		return nil
	}
	return []string{"windows_adapter_pending"}
}

func bindInstallTransaction(store *Store, request Request) (Transaction, Result, error) {
	transaction, err := store.ReadTransaction()
	if err != nil {
		return Transaction{}, Result{}, err
	}
	want := strings.TrimSpace(request.TransactionID)
	if want != "" && transaction.TransactionID != want {
		return Transaction{}, Result{}, fmt.Errorf("install 事务不匹配：journal=%s, 请求=%s", transaction.TransactionID, want)
	}
	current, err := store.ReadResult(transaction.TransactionID)
	if err != nil {
		current = resultFromTransaction(transaction)
	}
	if current.TransactionID != "" && current.TransactionID != transaction.TransactionID {
		current = resultFromTransaction(transaction)
	}
	return transaction, current, nil
}

func (engine Engine) commit(ctx context.Context, store *Store, request Request) (Result, error) {
	transaction, current, err := bindInstallTransaction(store, request)
	if err != nil {
		return Result{}, fmt.Errorf("commit: %w", err)
	}
	if transaction.Action == ActionUninstall {
		current.Warnings = removeWarning(current.Warnings, "windows_adapter_pending")
	}
	if request.RequireHealth {
		if err := verifyTransactionHealth(ctx, transaction, request, transaction.TargetVersion); err != nil {
			return current, err
		}
		current.Healthy = true
	}
	if transaction.State == updateengine.StateCommitted && current.TransactionID == transaction.TransactionID {
		if err := commitWindowsActivePointer(transaction.InstallRoot, transaction.TransactionID); err != nil {
			return current, err
		}
		if request.RequireHealth {
			current, err = store.Complete(transaction, updateengine.StateCommitted, current)
			if err != nil {
				return current, err
			}
			if warnings := cleanupCommittedGenerations(transaction, current); len(warnings) > 0 {
				updated, warningErr := store.AppendTerminalWarnings(transaction.TransactionID, warnings...)
				if warningErr != nil {
					current.Warnings = append(current.Warnings, "generation cleanup warning persistence failed: "+warningErr.Error())
				} else {
					current = updated
				}
			}
		}
		if !request.KeepJournal {
			discardJournal(store.Root(), transaction.TransactionID)
		}
		return current, nil
	}
	if transaction.State != updateengine.StateTrial {
		return current, fmt.Errorf("install commit 只能结束 trial，当前 state=%s transaction=%s", transaction.State, transaction.TransactionID)
	}
	return commitPreparedInstallWithJournal(store, transaction, current, request.KeepJournal)
}

func (engine Engine) abandon(ctx context.Context, store *Store, request Request) (Result, error) {
	transaction, current, err := bindInstallTransaction(store, request)
	if err != nil {
		return Result{}, fmt.Errorf("abandon: %w", err)
	}

	seal := func(state updateengine.State, code, message string, restored bool) (Result, error) {
		current.Failure = &updateengine.Failure{Code: code, Message: message, At: time.Now().UTC()}
		current = projectRestoredResult(current, transaction, request, restored)
		current.Healthy = restored && request.RequireHealth
		transaction.Phase = PhaseRollback
		transaction.ActiveVersion = current.ActiveVersion
		transaction.FallbackVersion = current.FallbackVersion
		if state == updateengine.StateRolledBack {
			return sealRolledBackInstall(store, transaction, current, nil)
		}
		completed, err := store.Complete(transaction, state, current)
		if err != nil {
			return current, err
		}
		return completed, nil
	}

	if request.RollbackFailed {
		return seal(updateengine.StateFailed, FailureExternalRollbackFailed, "OS adapter 回滚外部状态失败，权威事务不能写成 rolled_back", false)
	}
	if request.RequireHealth {
		if err := verifyTransactionHealth(ctx, transaction, request, transaction.SourceVersion); err != nil {
			failed, recordErr := seal(updateengine.StateFailed, FailureExternalRollbackFailed, err.Error(), false)
			return failed, errors.Join(err, recordErr)
		}
	}
	if current.TransactionID == transaction.TransactionID && current.State == updateengine.StateRolledBack && current.Phase == PhaseRollback {
		if err := releaseWindowsTrialPointer(transaction.InstallRoot, transaction.TransactionID); err != nil {
			return seal(updateengine.StateFailed, FailureRollbackFailed, "release windows trial pointer: "+err.Error(), false)
		}
		current = projectRestoredResult(current, transaction, request, true)
		current.Healthy = request.RequireHealth
		transaction.ActiveVersion = current.ActiveVersion
		transaction.FallbackVersion = current.FallbackVersion
		completed, err := store.Complete(transaction, updateengine.StateRolledBack, current)
		if err != nil {
			return current, err
		}
		discardJournal(store.Root(), transaction.TransactionID)
		return completed, nil
	}
	if current.TransactionID == transaction.TransactionID && current.State == updateengine.StateFailed {
		if isExternalRollbackFailure(transaction) {
			return seal(updateengine.StateRolledBack, FailureAbandoned, "operator confirmed external rollback is complete", true)
		}
		return current, nil
	}
	switch transaction.State {
	case updateengine.StateCommitted, updateengine.StateTrial, updateengine.StateStaged, updateengine.StateRollingBack, updateengine.StateRolledBack:
	default:
		return current, fmt.Errorf("install abandon 不能处理 state=%s", transaction.State)
	}
	return seal(updateengine.StateRolledBack, FailureAbandoned, "OS adapter 已完成外部回滚，权威状态撤销为 rolled_back", true)
}

func (engine Engine) uninstall(ctx context.Context, store *Store, request Request) (Result, error) {
	platform := currentPlatform()
	sourceVersion := existingVersion(request)
	transaction, err := store.ReadTransaction()
	if err == nil && transaction.Action == ActionUninstall && transaction.State == updateengine.StateTrial {
		if err := ensureUninstallIntentMatches(transaction, request); err != nil {
			return Result{}, err
		}
	} else {
		transaction, err = newTransaction(request, platform, sourceVersion)
		if err != nil {
			return Result{}, err
		}
	}
	transaction.Phase = PhaseRollback
	if err := store.WriteTransaction(transaction); err != nil {
		return Result{}, err
	}
	uninstallTaskName := windowsManagedTaskName(request)

	fail := func(err error) (Result, error) {
		result := Result{
			Failure:       &updateengine.Failure{Code: FailureUninstallFailed, Message: err.Error(), At: time.Now().UTC()},
			Version:       request.Version,
			ActiveVersion: sourceVersion,
			Healthy:       false,
		}
		completed, completeErr := store.Complete(transaction, updateengine.StateFailed, result)
		if completeErr != nil {
			return result, errors.Join(err, completeErr)
		}
		return completed, err
	}

	if err := uninstallPlatform(ctx, request); err != nil {
		return fail(err)
	}
	if request.PurgeConfig && !request.PurgeData {
		if err := purgeInstallConfig(request); err != nil {
			return fail(err)
		}
	}
	if request.PurgeData {
		if err := purgeInstallData(request); err != nil {
			return fail(err)
		}
		now := time.Now().UTC()
		return Result{
			SchemaVersion: SchemaVersion,
			TransactionID: transaction.TransactionID,
			Platform:      platform,
			Action:        ActionUninstall,
			State:         updateengine.StateCommitted,
			Phase:         PhaseCommit,
			Version:       request.Version,
			ActiveVersion: sourceVersion,
			Healthy:       false,
			TaskName:      uninstallTaskName,
			StartedAt:     transaction.StartedAt,
			CompletedAt:   now,
		}, nil
	}

	result := Result{
		Version:       request.Version,
		ActiveVersion: sourceVersion,
		Healthy:       false,
		TaskName:      uninstallTaskName,
		Warnings:      windowsUninstallAdapterWarnings(request),
	}
	if request.DeferCommit {
		transaction.State = updateengine.StateTrial
		transaction.Phase = PhaseCommit
		if err := store.WriteTransaction(transaction); err != nil {
			return result, err
		}
		result.SchemaVersion = SchemaVersion
		result.TransactionID = transaction.TransactionID
		result.Platform = platform
		result.Action = ActionUninstall
		result.State = updateengine.StateTrial
		result.Phase = PhaseCommit
		result.StartedAt = transaction.StartedAt
		if err := store.WriteResult(result); err != nil {
			return result, err
		}
		return result, nil
	}

	completed, err := store.Complete(transaction, updateengine.StateCommitted, result)
	if err != nil {
		return result, err
	}
	discardJournal(store.Root(), transaction.TransactionID)
	return completed, nil
}

func runtimeGOOS() string {
	return currentPlatform()
}

func bootstrapSkills(ctx context.Context, request Request, executable, bundleDir string) error {
	home := skillStorageHome(request)
	if home == "" {
		return nil
	}
	handled, err := tryBootstrapAsServiceUser(ctx, request, executable, home, bundleDir)
	if handled || err != nil {
		return err
	}
	cfg, err := config.StorageConfig(home)
	if err != nil {
		return err
	}
	stateDir, err := config.SkillStateDir(cfg)
	if err != nil {
		return err
	}
	state, err := skillstate.New(stateDir)
	if err != nil {
		return err
	}
	manager, err := skills.New(state)
	if err != nil {
		return err
	}
	_, err = skillbundle.Bootstrap(ctx, state, manager, bundleDir)
	return err
}

func skillStorageHome(request Request) string {
	home := strings.TrimSpace(request.AgentDockHome)
	if home == "" && strings.TrimSpace(request.DataDir) != "" {
		home = filepath.Join(request.DataDir, ".agentdock")
	}
	return home
}

func verifyRequest(request Request) error {
	if request.Action == ActionUninstall {
		return nil
	}
	if request.PayloadDir == "" && request.BinaryPath == "" {
		if request.Action == ActionRepair {
			return errors.New("repair 找不到已安装的 binary；请提供 --payload-dir 或 --binary")
		}
		return errors.New("install 需要 --payload-dir 或 --binary")
	}
	if request.PayloadDir != "" {
		info, err := os.Stat(request.PayloadDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("payload-dir 无效：%s", request.PayloadDir)
		}
	}
	if request.BinaryPath != "" {
		info, err := os.Stat(request.BinaryPath)
		if err != nil || info.IsDir() {
			return fmt.Errorf("binary 无效：%s", request.BinaryPath)
		}
	}
	if request.TunnelMode == "named" && strings.TrimSpace(request.ServerURL) == "" {
		return errors.New("Named Tunnel 必须提供 --server-url")
	}
	return nil
}

func existingVersion(request Request) string {
	store, err := NewStore(request.StateRoot())
	if err != nil {
		return windowsCommittedGeneration(request)
	}
	transaction, err := store.ReadTransaction()
	if err == nil {
		if version := versionFromInstallTransaction(transaction); version != "" {
			return version
		}
	}
	current, err := store.ReadCurrentResult()
	if err == nil {
		if version := versionFromInstallResult(current); version != "" {
			return version
		}
	}
	return windowsCommittedGeneration(request)
}

func versionFromInstallTransaction(transaction Transaction) string {
	switch transaction.Action {
	case ActionUninstall:
		return knownInstallVersion(transaction.SourceVersion, transaction.ActiveVersion)
	}
	switch transaction.State {
	case updateengine.StateCommitted:
		if version := knownInstallVersion(transaction.ActiveVersion, transaction.TargetVersion); version != "" {
			return version
		}
	case updateengine.StateRolledBack, updateengine.StateFailed, updateengine.StateTrial, updateengine.StateStaged, updateengine.StateRollingBack:
		return knownInstallVersion(transaction.SourceVersion)
	}
	return ""
}

func versionFromInstallResult(result Result) string {
	if result.Action == ActionUninstall {
		return knownInstallVersion(result.ActiveVersion)
	}
	if result.State != updateengine.StateCommitted {
		return ""
	}
	return knownInstallVersion(result.ActiveVersion, result.Version)
}

func knownInstallVersion(candidates ...string) string {
	for _, candidate := range candidates {
		version := strings.TrimSpace(candidate)
		if version != "" && version != "unknown" && version != "uninstalled" {
			return version
		}
	}
	return ""
}

func isExternalRollbackFailure(transaction Transaction) bool {
	if transaction.State != updateengine.StateFailed || transaction.Failure == nil {
		return false
	}
	switch transaction.Failure.Code {
	case FailureExternalRollbackFailed:
		return true
	case FailureRollbackFailed:
		return strings.Contains(transaction.Failure.Message, "OS adapter")
	default:
		return false
	}
}

func projectRestoredResult(result Result, transaction Transaction, request Request, restored bool) Result {
	result.Healthy = false
	result.ActiveVersion = transaction.SourceVersion
	result.FallbackVersion = transaction.SourceVersion
	result.LocalMCPURL = ""
	result.PublicURL = ""
	result.PrivilegeMode = ""
	if !restored {
		return result
	}
	activated, err := readActivatedInstall(request)
	if err != nil {
		return result
	}
	result.LocalMCPURL = activated.LocalMCPURL
	result.PublicURL = activated.PublicURL
	result.PrivilegeMode = activated.PrivilegeMode
	return result
}

func windowsCommittedGeneration(request Request) string {
	store, err := updateengine.NewStore(request.InstallRoot)
	if err != nil {
		return ""
	}
	active, err := store.ReadActive()
	if err != nil || active.State != updateengine.StateCommitted {
		return ""
	}
	return strings.TrimSpace(active.ActiveVersion)
}

func repairBinary(request Request) string {
	if runtimeGOOS() == "windows" {
		if layout, err := updateengine.NewWindowsLayout(request.InstallRoot); err == nil {
			if fileExists(layout.CoreShim()) {
				return layout.CoreShim()
			}
			if version := existingVersion(request); version != "" && fileExists(layout.GenerationCore(version)) {
				return layout.GenerationCore(version)
			}
		}
		for _, candidate := range []string{
			filepath.Join(request.InstallRoot, "bin", "agentdock.exe"),
			filepath.Join(request.InstallRoot, "agentdock.exe"),
		} {
			if fileExists(candidate) {
				return candidate
			}
		}
		return ""
	}
	live := unixLiveBinary(request)
	if fileExists(live) {
		return live
	}
	nested := filepath.Join(request.InstallRoot, "bin", "agentdock")
	if fileExists(nested) {
		return nested
	}
	direct := filepath.Join(request.InstallRoot, "agentdock")
	if fileExists(direct) {
		return direct
	}
	return ""
}

func randomHex(byteCount int) (string, error) {
	raw := make([]byte, byteCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func copyTree(src, dst string, mode os.FileMode) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), mode); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = info.Mode()
	}
	return os.WriteFile(dst, data, mode)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
