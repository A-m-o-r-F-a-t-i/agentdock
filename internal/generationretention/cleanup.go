package generationretention

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const maxStateFileBytes = 1 << 20

var ErrMissingHealthyFallback = errors.New("committed upgrade has no verified fallback generation")

type stateRecord struct {
	TransactionID   string `json:"transaction_id"`
	SourceVersion   string `json:"source_version"`
	TargetVersion   string `json:"target_version"`
	ActiveVersion   string `json:"active_version"`
	FallbackVersion string `json:"fallback_version"`
	State           string `json:"state"`
}

type journalRecord struct {
	Backups []struct {
		Original string `json:"original"`
	} `json:"backups"`
	Created        []string `json:"created"`
	RestoreEntries []struct {
		Original string `json:"original"`
	} `json:"restore_entries"`
}

// CollectPolicy reads the authoritative generation pointer plus the current
// Installer and Update Engine transactions. Any malformed authoritative state
// blocks cleanup: retaining extra bytes is safer than deleting rollback data.
func CollectPolicy(stateRoot, versionsDir string) (Policy, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	versionsDir = strings.TrimSpace(versionsDir)
	if stateRoot == "" || versionsDir == "" {
		return Policy{}, errors.New("generation retention state root and versions directory are required")
	}
	root, err := filepath.Abs(stateRoot)
	if err != nil {
		return Policy{}, fmt.Errorf("resolve generation retention state root: %w", err)
	}
	versions, err := filepath.Abs(versionsDir)
	if err != nil {
		return Policy{}, fmt.Errorf("resolve generation versions directory: %w", err)
	}

	active, err := readStateRecord(filepath.Join(root, "active-version.json"), true)
	if err != nil {
		return Policy{}, fmt.Errorf("read active generation pointer: %w", err)
	}
	if canonicalVersion(active.ActiveVersion) == "" {
		return Policy{}, errors.New("active generation pointer has no safe active version")
	}
	policy := Policy{
		VersionsDir:     filepath.Clean(versions),
		ActiveVersion:   active.ActiveVersion,
		FallbackVersion: active.FallbackVersion,
	}
	protected := map[string]string{}
	protectVersion(protected, active.ActiveVersion)
	protectVersion(protected, active.FallbackVersion)

	for _, relative := range []string{
		filepath.Join("install", "transaction.json"),
		filepath.Join("update", "transaction.json"),
	} {
		record, readErr := readStateRecord(filepath.Join(root, relative), false)
		if readErr != nil {
			return Policy{}, fmt.Errorf("read generation transaction %s: %w", relative, readErr)
		}
		if record == nil {
			continue
		}
		journalVersions, journalPresent, journalErr := collectJournalVersions(root, record.TransactionID, versions)
		if journalErr != nil {
			return Policy{}, fmt.Errorf("read generation rollback journal for %s: %w", record.TransactionID, journalErr)
		}
		for _, version := range journalVersions {
			protectVersion(protected, version)
		}
		protectTransaction := !terminalState(record.State) || journalPresent || sameVersion(record.TargetVersion, active.ActiveVersion)
		if protectTransaction {
			protectVersion(protected, record.ActiveVersion)
			protectVersion(protected, record.FallbackVersion)
			protectVersion(protected, record.SourceVersion)
			protectVersion(protected, record.TargetVersion)
		}

		// A committed cross-version transaction without an explicit fallback is
		// not evidence that its source is healthy. Refuse to guess and keep every
		// existing generation until a verified pointer is written.
		if strings.EqualFold(strings.TrimSpace(record.State), "committed") &&
			nonEmptyDifferent(record.SourceVersion, record.TargetVersion) &&
			sameVersion(record.TargetVersion, active.ActiveVersion) &&
			strings.TrimSpace(active.FallbackVersion) == "" &&
			strings.TrimSpace(record.FallbackVersion) == "" {
			return Policy{}, fmt.Errorf("%w: source=%s target=%s", ErrMissingHealthyFallback, record.SourceVersion, record.TargetVersion)
		}
	}

	for _, version := range protected {
		if !sameVersion(version, policy.ActiveVersion) && !sameVersion(version, policy.FallbackVersion) {
			policy.ProtectedVersions = append(policy.ProtectedVersions, version)
		}
	}
	sort.Slice(policy.ProtectedVersions, func(i, j int) bool {
		return canonicalVersion(policy.ProtectedVersions[i]) < canonicalVersion(policy.ProtectedVersions[j])
	})
	return policy, nil
}

// Clean removes only immediate, unreferenced generation directories. Removal
// failures are reported as warnings and are safe to retry.
func Clean(policy Policy) Report {
	return clean(policy, os.RemoveAll)
}

func clean(policy Policy, removeAll func(string) error) Report {
	report := Report{}
	versionsDir, err := filepath.Abs(strings.TrimSpace(policy.VersionsDir))
	if err != nil || strings.TrimSpace(policy.VersionsDir) == "" {
		report.Warnings = append(report.Warnings, "generation cleanup skipped: invalid versions directory")
		return report
	}
	versionsDir = filepath.Clean(versionsDir)

	protected := map[string]string{}
	protectVersion(protected, policy.ActiveVersion)
	protectVersion(protected, policy.FallbackVersion)
	for _, version := range policy.ProtectedVersions {
		protectVersion(protected, version)
	}
	for _, version := range protected {
		report.Protected = append(report.Protected, version)
	}
	sort.Slice(report.Protected, func(i, j int) bool {
		return canonicalVersion(report.Protected[i]) < canonicalVersion(report.Protected[j])
	})

	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("generation cleanup could not enumerate versions: %v", err))
		}
		return report
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name()) })
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		key := canonicalVersion(name)
		if key == "" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("generation cleanup skipped unsafe directory name %q", name))
			continue
		}
		if _, keep := protected[key]; keep {
			continue
		}
		path := filepath.Join(versionsDir, name)
		if !isImmediateChild(versionsDir, path) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("generation cleanup skipped path outside versions directory: %q", name))
			continue
		}
		removal := Removal{Version: normalizeVersion(name), Path: filepath.ToSlash(name)}
		if err := removeAll(path); err != nil {
			removal.Error = err.Error()
			report.Warnings = append(report.Warnings, fmt.Sprintf("generation %s could not be removed: %v", removal.Version, err))
		} else if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			if err == nil {
				removal.Error = "path still exists after removal"
			} else {
				removal.Error = err.Error()
			}
			report.Warnings = append(report.Warnings, fmt.Sprintf("generation %s removal could not be verified: %s", removal.Version, removal.Error))
		}
		report.Removed = append(report.Removed, removal)
	}
	return report
}

func readStateRecord(path string, required bool) (*stateRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxStateFileBytes {
		return nil, fmt.Errorf("state file has invalid size or type")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	var record stateRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func collectJournalVersions(root, transactionID, versionsDir string) ([]string, bool, error) {
	transactionID = strings.TrimSpace(transactionID)
	if transactionID == "" || filepath.Base(transactionID) != transactionID {
		return nil, false, nil
	}
	path := filepath.Join(root, "install", "rollback", transactionID, "journal.json")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, true, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxStateFileBytes {
		return nil, true, errors.New("rollback journal has invalid size or type")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, true, err
	}
	var journal journalRecord
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, true, err
	}
	protected := map[string]string{}
	for _, backup := range journal.Backups {
		protectGenerationPath(protected, versionsDir, backup.Original)
	}
	for _, path := range journal.Created {
		protectGenerationPath(protected, versionsDir, path)
	}
	for _, restore := range journal.RestoreEntries {
		protectGenerationPath(protected, versionsDir, restore.Original)
	}
	versions := make([]string, 0, len(protected))
	for _, version := range protected {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return canonicalVersion(versions[i]) < canonicalVersion(versions[j]) })
	return versions, true, nil
}

func protectGenerationPath(protected map[string]string, versionsDir, path string) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return
	}
	relative, err := filepath.Rel(filepath.Clean(versionsDir), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if len(parts) == 0 {
		return
	}
	protectVersion(protected, parts[0])
}

func terminalState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "committed", "rolled_back":
		return true
	default:
		return false
	}
}

func protectVersion(protected map[string]string, version string) {
	key := canonicalVersion(version)
	if key == "" {
		return
	}
	if _, exists := protected[key]; !exists {
		protected[key] = normalizeVersion(version)
	}
}

func canonicalVersion(version string) string {
	version = normalizeVersion(version)
	if version == "" || strings.ContainsAny(version, `/\\<>:"|?*`) || version == "v." || strings.HasPrefix(version, "v..") || strings.HasSuffix(version, ".") {
		return ""
	}
	for _, r := range version {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ""
		}
	}
	return strings.ToLower(version)
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	version = strings.TrimPrefix(version, "V")
	if version == "" {
		return ""
	}
	return "v" + version
}

func sameVersion(left, right string) bool {
	leftKey, rightKey := canonicalVersion(left), canonicalVersion(right)
	return leftKey != "" && leftKey == rightKey
}

func nonEmptyDifferent(left, right string) bool {
	leftKey, rightKey := canonicalVersion(left), canonicalVersion(right)
	return leftKey != "" && rightKey != "" && leftKey != rightKey
}

func isImmediateChild(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return filepath.Dir(relative) == "."
}
