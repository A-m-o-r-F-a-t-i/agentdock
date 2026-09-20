package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

const maxRegistryBytes = 1 << 20
const maxWorkspaces = 128

var workspaceIDPattern = regexp.MustCompile(`^wsp_[a-f0-9]{16}$`)

var (
	ErrWorkspaceRequired = errors.New("workspace_required")
	ErrWorkspaceNotFound = errors.New("workspace_not_found")
	ErrWorkspaceConflict = errors.New("workspace_revision_conflict")
	ErrWorkspaceBoundary = errors.New("workspace_path_outside_root")
)

type Record struct {
	ID             string    `json:"workspace_id"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	Project        string    `json:"project,omitempty"`
	Runtime        string    `json:"runtime"`
	Distribution   string    `json:"wsl_distribution,omitempty"`
	Root           string    `json:"root"`
	DefaultWorkdir string    `json:"default_workdir"`
	ArtifactRoot   string    `json:"artifact_root,omitempty"`
	ScratchRoot    string    `json:"scratch_root,omitempty"`
	CacheRoot      string    `json:"cache_root,omitempty"`
	RulesRevision  int       `json:"rules_revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type RegisterInput struct {
	WorkspaceID      string `json:"workspace_id,omitempty"`
	ExpectedRevision *int   `json:"expected_revision,omitempty"`
	Name             string `json:"name,omitempty"`
	Kind             string `json:"kind,omitempty"`
	Project          string `json:"project,omitempty"`
	Runtime          string `json:"runtime,omitempty"`
	Distribution     string `json:"wsl_distribution,omitempty"`
	Root             string `json:"root,omitempty"`
	DefaultWorkdir   string `json:"default_workdir,omitempty"`
	ArtifactRoot     string `json:"artifact_root,omitempty"`
	ScratchRoot      string `json:"scratch_root,omitempty"`
	CacheRoot        string `json:"cache_root,omitempty"`
	CreateRoot       bool   `json:"create_root,omitempty"`
}

type registryState struct {
	SchemaVersion int      `json:"schema_version"`
	DefaultID     string   `json:"default_workspace_id"`
	Records       []Record `json:"workspaces"`
}

type Registry struct {
	home        string
	storageRoot string
	mu          sync.Mutex
}

func HostRuntime() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	return "unix"
}

func NewRegistry(home, defaultRoot string) (*Registry, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(defaultRoot) {
		return nil, ErrWorkspaceRequired
	}
	root, err := canonicalNativePath(defaultRoot)
	if err != nil {
		return nil, err
	}
	if err = directoryExists(root); err != nil {
		return nil, err
	}
	registry := &Registry{home: filepath.Clean(home), storageRoot: root}
	if err = os.MkdirAll(registry.home, 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := registry.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	state, err := registry.read()
	if err != nil {
		return nil, err
	}
	record, found := findRoot(state, root, HostRuntime(), "")
	if !found {
		record, err = registry.makeRecord(RegisterInput{Root: root}, nil)
		if err != nil {
			return nil, err
		}
		state.Records = append(state.Records, record)
	}
	if state.DefaultID != record.ID || !found {
		state.DefaultID = record.ID
		if err = registry.write(state); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	release, err := filelock.Acquire(bounded, filepath.Join(r.home, ".workspaces.lock"))
	cancel()
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	return func() { release(); r.mu.Unlock() }, nil
}

func (r *Registry) read() (registryState, error) {
	state := registryState{SchemaVersion: 1, Records: []Record{}}
	file := filepath.Join(r.home, "workspaces.json")
	if info, err := os.Lstat(file); err == nil && !info.Mode().IsRegular() {
		return state, errors.New("workspace registry must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return state, err
	}
	input, err := os.Open(file)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, maxRegistryBytes+1))
	if err != nil {
		return state, err
	}
	if len(data) > maxRegistryBytes {
		return state, errors.New("workspace registry exceeds size limit")
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("read workspace registry: %w", err)
	}
	if state.SchemaVersion != 1 || len(state.Records) > maxWorkspaces {
		return state, errors.New("invalid workspace registry schema or size")
	}
	ids := map[string]bool{}
	for _, record := range state.Records {
		if err = validateRecord(record); err != nil {
			return state, err
		}
		if ids[record.ID] {
			return state, errors.New("duplicate workspace id")
		}
		ids[record.ID] = true
	}
	if state.DefaultID != "" && !ids[state.DefaultID] {
		return state, errors.New("default workspace is missing from registry")
	}
	return state, nil
}

func (r *Registry) write(state registryState) error {
	if len(state.Records) > maxWorkspaces {
		return errors.New("workspace registry limit reached")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxRegistryBytes {
		return errors.New("workspace registry exceeds size limit")
	}
	return atomicfile.Write(filepath.Join(r.home, "workspaces.json"), append(data, '\n'), 0600)
}

func (r *Registry) List(ctx context.Context) ([]Record, string, error) {
	release, err := r.lock(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	state, err := r.read()
	if err != nil {
		return nil, "", err
	}
	return append([]Record{}, state.Records...), state.DefaultID, nil
}

func (r *Registry) Select(ctx context.Context, id, project string) (Record, error) {
	release, err := r.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer release()
	state, err := r.read()
	if err != nil {
		return Record{}, err
	}
	if id != "" && !workspaceIDPattern.MatchString(id) {
		return Record{}, ErrWorkspaceNotFound
	}
	if id == "" && project != "" {
		matches := []Record{}
		for _, record := range state.Records {
			if strings.EqualFold(record.Project, project) || strings.EqualFold(record.Name, project) {
				matches = append(matches, record)
			}
		}
		if len(matches) > 1 {
			return Record{}, errors.New("workspace_project_ambiguous: provide workspace_id")
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	if id == "" {
		id = state.DefaultID
	}
	if id == "" {
		return Record{}, ErrWorkspaceRequired
	}
	for _, record := range state.Records {
		if record.ID == id {
			return record, nil
		}
	}
	return Record{}, ErrWorkspaceNotFound
}

// EnsureRoot is used only for an explicit native context workdir. It does not change the
// global default workspace or rewrite an existing record's custom paths and project name.
func (r *Registry) EnsureRoot(ctx context.Context, root string) (Record, error) {
	normalized, err := canonicalNativePath(root)
	if err != nil {
		return Record{}, err
	}
	if err = directoryExists(normalized); err != nil {
		return Record{}, err
	}
	release, err := r.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer release()
	state, err := r.read()
	if err != nil {
		return Record{}, err
	}
	if record, found := findRoot(state, normalized, HostRuntime(), ""); found {
		return record, nil
	}
	record, err := r.makeRecord(RegisterInput{Root: normalized}, nil)
	if err != nil {
		return Record{}, err
	}
	state.Records = append(state.Records, record)
	if err = r.write(state); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (r *Registry) Register(ctx context.Context, input RegisterInput) (Record, error) {
	release, err := r.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer release()
	state, err := r.read()
	if err != nil {
		return Record{}, err
	}
	var previous *Record
	index := -1
	if input.WorkspaceID != "" {
		for i, record := range state.Records {
			if record.ID == input.WorkspaceID {
				copy := record
				previous = &copy
				index = i
				break
			}
		}
		if previous == nil {
			return Record{}, ErrWorkspaceNotFound
		}
		if input.ExpectedRevision == nil || *input.ExpectedRevision != previous.RulesRevision {
			return Record{}, ErrWorkspaceConflict
		}
	} else if input.ExpectedRevision != nil {
		return Record{}, errors.New("workspace_id is required with expected_revision")
	}
	record, err := r.makeRecord(input, previous)
	if err != nil {
		return Record{}, err
	}
	if existing, found := findRoot(state, record.Root, record.Runtime, record.Distribution); found && existing.ID != record.ID {
		if previous == nil {
			return existing, nil
		}
		return Record{}, errors.New("another workspace already owns this runtime/root")
	}
	if previous != nil {
		state.Records[index] = record
	} else {
		state.Records = append(state.Records, record)
	}
	if err = r.write(state); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (r *Registry) makeRecord(input RegisterInput, previous *Record) (Record, error) {
	var record Record
	if previous != nil {
		record = *previous
	} else {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			return record, err
		}
		record = Record{ID: "wsp_" + hex.EncodeToString(id[:]), Kind: "repository", Runtime: HostRuntime(), DefaultWorkdir: ".", CreatedAt: time.Now().UTC()}
	}
	for source, target := range map[*string]*string{&input.Name: &record.Name, &input.Kind: &record.Kind, &input.Project: &record.Project, &input.Runtime: &record.Runtime, &input.Distribution: &record.Distribution, &input.Root: &record.Root, &input.DefaultWorkdir: &record.DefaultWorkdir, &input.ArtifactRoot: &record.ArtifactRoot, &input.ScratchRoot: &record.ScratchRoot, &input.CacheRoot: &record.CacheRoot} {
		if *source != "" {
			*target = strings.TrimSpace(*source)
		}
	}
	if record.Runtime != "wsl" && record.Runtime != HostRuntime() {
		return record, errors.New("workspace runtime must match this host or be explicit wsl")
	}
	if record.Runtime == "wsl" {
		if runtime.GOOS != "windows" {
			return record, errors.New("WSL workspaces require a Windows host")
		}
		if input.CreateRoot {
			return record, errors.New("create_root is available only for native workspaces")
		}
		if !validPOSIXRoot(record.Root) {
			return record, ErrWorkspaceRequired
		}
		record.Root = path.Clean(record.Root)
	} else {
		var err error
		record.Root, err = canonicalNativePath(record.Root)
		if err != nil {
			return record, err
		}
		if previous == nil {
			if record.ArtifactRoot == "" {
				record.ArtifactRoot = filepath.Join(r.storageRoot, "artifacts", record.ID)
			}
			if record.ScratchRoot == "" {
				record.ScratchRoot = filepath.Join(r.storageRoot, "scratch", record.ID)
			}
			if record.CacheRoot == "" {
				record.CacheRoot = filepath.Join(r.storageRoot, "cache")
			}
		}
	}
	if record.Name == "" {
		if record.Runtime == "wsl" {
			record.Name = path.Base(record.Root)
		} else {
			record.Name = filepath.Base(record.Root)
		}
	}
	record.RulesRevision++
	record.UpdatedAt = time.Now().UTC()
	if err := validateRecord(record); err != nil {
		return record, err
	}
	if record.Runtime != "wsl" {
		if input.CreateRoot {
			if err := os.MkdirAll(record.Root, 0755); err != nil {
				return record, err
			}
		}
		if err := directoryExists(record.Root); err != nil {
			return record, err
		}
	}
	return record, nil
}

func validateRecord(record Record) error {
	if !workspaceIDPattern.MatchString(record.ID) || record.RulesRevision < 1 {
		return errors.New("invalid workspace identifier or revision")
	}
	if strings.TrimSpace(record.Name) == "" || len(record.Name) > 160 || len(record.Project) > 160 || len(record.Distribution) > 128 {
		return errors.New("invalid workspace name, project or distribution")
	}
	if record.Kind != "repository" && record.Kind != "directory" {
		return errors.New("invalid workspace kind")
	}
	if record.Runtime != "wsl" && record.Runtime != HostRuntime() {
		return errors.New("unsupported workspace runtime")
	}
	return validateRecordPaths(record)
}

func validateRecordPaths(record Record) error {
	if record.DefaultWorkdir == "" || len(record.DefaultWorkdir) > 2048 || strings.ContainsRune(record.DefaultWorkdir, 0) {
		return errors.New("valid workspace default_workdir is required")
	}
	for _, value := range []string{record.Root, record.ArtifactRoot, record.ScratchRoot, record.CacheRoot} {
		if value == "" {
			if value == record.Root {
				return ErrWorkspaceRequired
			}
			continue
		}
		if len(value) > 2048 || strings.ContainsRune(value, 0) {
			return errors.New("invalid workspace path")
		}
		if record.Runtime == "wsl" {
			if !validPOSIXRoot(value) {
				return ErrWorkspaceRequired
			}
		} else {
			if !filepath.IsAbs(value) || filepath.Dir(filepath.Clean(value)) == filepath.Clean(value) {
				return ErrWorkspaceRequired
			}
		}
	}
	if record.Runtime == "wsl" {
		if path.IsAbs(record.DefaultWorkdir) || strings.Contains(record.DefaultWorkdir, "\\") || outsideRelative(path.Clean(record.DefaultWorkdir)) {
			return ErrWorkspaceBoundary
		}
	} else if filepath.IsAbs(record.DefaultWorkdir) || outsideRelative(filepath.ToSlash(filepath.Clean(record.DefaultWorkdir))) {
		return ErrWorkspaceBoundary
	}
	return nil
}

func findRoot(state registryState, root, runtimeName, distribution string) (Record, bool) {
	for _, record := range state.Records {
		equal := record.Root == root
		if runtimeName == "windows" {
			equal = strings.EqualFold(record.Root, root)
		}
		if equal && record.Runtime == runtimeName && record.Distribution == distribution {
			return record, true
		}
	}
	return Record{}, false
}
