// Package generationretention defines the bounded cleanup contract for
// immutable Windows runtime generations. Callers remain responsible for
// proving health and committing their transaction before invoking cleanup.
package generationretention

// Policy is a complete snapshot of generation references that must survive a
// cleanup attempt. VersionsDir is scanned only at its immediate child level.
type Policy struct {
	VersionsDir       string
	ActiveVersion     string
	FallbackVersion   string
	ProtectedVersions []string
}

// Removal records one old generation selected by the policy. Error is set
// when deletion was denied or otherwise failed; such a failure is a warning
// and must not invalidate an already committed healthy installation.
type Removal struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	Error   string `json:"error,omitempty"`
}

// Report is deterministic and safe to persist in diagnostics. Paths are
// installation-local generation paths and must not contain credentials.
type Report struct {
	Protected []string  `json:"protected,omitempty"`
	Removed   []Removal `json:"removed,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
}
