package activity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrConversationOwner    = errors.New("conversation belongs to another authenticated client")
	ErrConversationConflict = errors.New("conversation binding revision changed")
	ErrConversationTrashed  = errors.New("conversation is in the recycle bin; restore it before continuing")
	conversationIdentifier  = regexp.MustCompile(`^conv_[a-f0-9]{32}$`)
)

// Source is set by an authenticated transport. HostConversationID is correlation
// metadata, never an authorization credential. Raw host identifiers are not logged.
type Source struct {
	Principal          string
	Provider           string
	Namespace          string
	HostConversationID string
	HostTitle          string // Optional adapter-owned title; never a tool argument.
	ConnectionID       string
	// Set only by an adapter that guarantees one conversation per connection.
	ConnectionIsConversation bool
}
type sourceContextKey struct{}

func WithSource(ctx context.Context, source Source) context.Context {
	return context.WithValue(ctx, sourceContextKey{}, source)
}
func SourceFromContext(ctx context.Context) Source {
	source, _ := ctx.Value(sourceContextKey{}).(Source)
	if source.Principal == "" {
		source.Principal = "local"
	}
	if source.Namespace == "" {
		source.Namespace = "native"
	}
	if source.Provider == "" {
		source.Provider = "generic"
	}
	return source
}
func NewExecutionID(prefix string) (string, error) {
	switch prefix {
	case "conv_", "call_", "approval_":
	default:
		return "", errors.New("unsupported execution identifier prefix")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

// Management metadata is separate from execution truth and never names a file
// to delete. Archived/trash state therefore cannot move or remove a workspace.
type Management struct {
	Pinned     bool       `json:"pinned,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	TrashedAt  *time.Time `json:"trashed_at,omitempty"`
	PurgeAfter *time.Time `json:"purge_after,omitempty"`
}
type Conversation struct {
	Management
	ID           string            `json:"conversation_id"`
	Title        string            `json:"title"`
	TitleSource  string            `json:"title_source,omitempty"`
	TerminatedAt *time.Time        `json:"terminated_at,omitempty"`
	DeletedAt    *time.Time        `json:"deleted_at,omitempty"`
	Source       string            `json:"source"`
	Attribution  string            `json:"attribution"`
	State        ConversationState `json:"state"`
	TaskIDs      []string          `json:"task_ids,omitempty"`
	WorkspaceIDs []string          `json:"workspace_ids,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}
type conversationRecord struct {
	Conversation
	OwnerKey  string `json:"owner_key"`
	SourceKey string `json:"source_key,omitempty"`
}
type conversationState struct {
	SchemaVersion int                           `json:"schema_version"`
	Items         map[string]conversationRecord `json:"items"`
	sources       map[string]string
}
type ConversationRegistry struct {
	root string
	// Cache access uses the same cancellable file lock as disk access.
	cached        *conversationSnapshot
	readBuffer    []byte
	decodeCount   atomic.Uint64
	verifiedReads atomic.Uint64
	verifiedBytes atomic.Uint64
}

func NewConversationRegistry(root string) (*ConversationRegistry, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("conversation storage must be a real directory")
	}
	return &ConversationRegistry{root: abs}, nil
}
func sourceDigest(parts ...string) string {
	// The digest is the stable lookup key; it replaces persisting raw host IDs.
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r *ConversationRegistry) state(ctx context.Context, change func(*conversationState) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := filelock.Acquire(ctx, filepath.Join(r.root, ".conversations.lock"))
	if err != nil {
		return err
	}
	defer release()
	path := filepath.Join(r.root, "conversations.json")
	snapshot, err := r.readSnapshot(ctx, path)
	if err != nil {
		r.cached = nil
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The callback exclusively owns this state while locked. Any error,
	// partial mutation or failed persistence invalidates the cached view.
	r.cached = nil
	state := snapshot.state
	dirty, err := change(state)
	if err != nil {
		return err
	}
	if !dirty {
		r.cached = snapshot
		return nil
	}
	if len(state.Items) > 20000 {
		return errors.New("conversation registry capacity reached; clean archived metadata")
	}
	if err = indexConversationState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("conversation registry capacity reached")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data = append(data, '\n')
	if err = atomicfile.Write(path, data, 0600); err != nil {
		return err
	}
	r.cached = &conversationSnapshot{state: state, serialized: data, exists: true}
	return nil
}

// ConversationResolver consumes adapter-owned metadata, never tool arguments.
// A missing host identity remains an unattributed navigation group. Transport
// sessions are eligible only when the adapter explicitly guarantees 1:1 use.
type ConversationResolver struct{ Registry *ConversationRegistry }

func (r ConversationResolver) Resolve(ctx context.Context) (Conversation, error) {
	return r.Registry.Resolve(ctx)
}

func (r *ConversationRegistry) Resolve(ctx context.Context) (Conversation, error) {
	source := SourceFromContext(ctx)
	if len(source.Principal) > 4096 || len(source.Namespace) > 256 || len(source.Provider) > 128 || len(source.HostConversationID) > 1024 || len(source.ConnectionID) > 1024 || !utf8.ValidString(source.HostConversationID) || !utf8.ValidString(source.ConnectionID) {
		return Conversation{}, errors.New("invalid source metadata")
	}
	identity, quality := source.HostConversationID, "host_metadata"
	if identity == "" && source.ConnectionIsConversation && source.ConnectionID != "" {
		identity, quality = source.ConnectionID, "connection_fallback"
	}
	if identity == "" {
		return Conversation{Source: source.Namespace, Attribution: "unattributed"}, nil
	}
	owner := SourceOwnerKey(ctx)
	hostKey := sourceDigest(source.Provider, owner, source.Namespace, quality, identity)
	var resolved Conversation
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		if id, found := state.sources[hostKey]; found {
			record := state.Items[id]
			resolved = cloneConversation(record.Conversation)
			if record.DeletedAt != nil {
				return false, ErrConversationDeleted
			}
			if record.TrashedAt != nil {
				return false, ErrConversationTrashed
			}
			dirty := applyAutomaticTitle(&record.Conversation, source.HostTitle, "host")
			if dirty {
				state.Items[id] = record
				resolved = cloneConversation(record.Conversation)
			}
			return dirty, nil
		}
		id, err := NewExecutionID("conv_")
		if err != nil {
			return false, err
		}
		now := time.Now().UTC()
		record := conversationRecord{OwnerKey: owner, SourceKey: hostKey, Conversation: Conversation{
			ID: id, Title: "对话 · " + now.Local().Format("01-02 15:04"), TitleSource: "fallback", Source: source.Namespace, Attribution: quality, CreatedAt: now, UpdatedAt: now,
			State: ConversationState{BindingRevision: 1, UpdatedAt: now},
		}}
		applyAutomaticTitle(&record.Conversation, source.HostTitle, "host")
		state.Items[id] = record
		resolved = cloneConversation(record.Conversation)
		return true, nil
	})
	return resolved, err
}

// SourceOwnerKey is an audit correlation key, not an authorization credential.
// Its input must originate in the authenticated ingress, not MCP _meta.
func SourceOwnerKey(ctx context.Context) string {
	return sourceDigest("principal", SourceFromContext(ctx).Principal)
}

func (r *ConversationRegistry) Owns(ctx context.Context, id string) error {
	return r.state(ctx, func(state *conversationState) (bool, error) {
		record, found := state.Items[id]
		if !found {
			return false, ErrConversationNotFound
		}
		if record.OwnerKey != SourceOwnerKey(ctx) {
			return false, ErrConversationOwner
		}
		return false, nil
	})
}

func cloneConversation(item Conversation) Conversation {
	item.Tags = append([]string(nil), item.Tags...)
	item.TaskIDs = append([]string(nil), item.TaskIDs...)
	item.WorkspaceIDs = append([]string(nil), item.WorkspaceIDs...)
	copyTime := func(value *time.Time) *time.Time {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	item.ArchivedAt = copyTime(item.ArchivedAt)
	item.TrashedAt = copyTime(item.TrashedAt)
	item.PurgeAfter = copyTime(item.PurgeAfter)
	item.TerminatedAt = copyTime(item.TerminatedAt)
	item.DeletedAt = copyTime(item.DeletedAt)
	return item
}
func (r *ConversationRegistry) Get(ctx context.Context, id string) (Conversation, error) {
	var item Conversation
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		record, found := state.Items[id]
		if !found {
			return false, ErrConversationNotFound
		}
		if record.DeletedAt != nil {
			return false, ErrConversationNotFound
		}
		item = cloneConversation(record.Conversation)
		return false, nil
	})
	return item, err
}

// List is an internal metadata snapshot. HTTP pagination is applied after
// merging last-activity timestamps from the single call projection.
func (r *ConversationRegistry) List(ctx context.Context) ([]Conversation, error) {
	items := []Conversation{}
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		for _, record := range state.Items {
			if record.DeletedAt != nil {
				continue
			}
			items = append(items, cloneConversation(record.Conversation))
		}
		return false, nil
	})
	return items, err
}
func (r *ConversationRegistry) Link(ctx context.Context, id, taskID, workspaceID string) error {
	if id == "" {
		return nil
	}
	if taskID != "" && !identifier.MatchString(taskID) {
		return errors.New("invalid linked task_id")
	}
	if workspaceID != "" && !identifier.MatchString(workspaceID) {
		return errors.New("invalid linked workspace_id")
	}
	return r.state(ctx, func(state *conversationState) (bool, error) {
		record, found := state.Items[id]
		if !found {
			return false, ErrConversationNotFound
		}
		dirty := false
		appendUnique := func(items *[]string, value string) {
			if value == "" {
				return
			}
			for _, item := range *items {
				if item == value {
					return
				}
			}
			*items = append(*items, value)
			dirty = true
		}
		appendUnique(&record.TaskIDs, taskID)
		appendUnique(&record.WorkspaceIDs, workspaceID)
		if dirty {
			record.UpdatedAt = time.Now().UTC()
			state.Items[id] = record
		}
		return dirty, nil
	})
}

type MetadataChange struct {
	Action        string   `json:"action"`
	Title         string   `json:"title,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	RetentionDays int      `json:"retention_days,omitempty"`
}

func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) > 24 {
		return nil, errors.New("at most 24 tags are allowed")
	}
	result := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if len(tag) > 128 || !utf8.ValidString(tag) || strings.ContainsAny(tag, "\r\n\x00") {
			return nil, errors.New("invalid tag")
		}
		if !seen[tag] {
			seen[tag] = true
			result = append(result, tag)
		}
	}
	sort.Strings(result)
	return result, nil
}
func ApplyManagement(metadata *Management, title *string, change MetadataChange, now time.Time) error {
	switch change.Action {
	case "rename":
		value := strings.TrimSpace(change.Title)
		if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("title must contain 1–512 UTF-8 bytes on one line")
		}
		*title = value
	case "pin":
		metadata.Pinned = true
	case "unpin":
		metadata.Pinned = false
	case "tags":
		tags, err := NormalizeTags(change.Tags)
		if err != nil {
			return err
		}
		metadata.Tags = tags
	case "archive":
		metadata.ArchivedAt = &now
	case "unarchive":
		metadata.ArchivedAt = nil
	case "trash":
		days := change.RetentionDays
		if days == 0 {
			days = 30
		}
		if days < 1 || days > 3650 {
			return errors.New("recycle-bin retention must be 1–3650 days")
		}
		if metadata.TrashedAt == nil {
			until := now.AddDate(0, 0, days)
			metadata.TrashedAt = &now
			metadata.PurgeAfter = &until
		}
	case "restore":
		metadata.TrashedAt = nil
		metadata.PurgeAfter = nil
	default:
		return errors.New("unsupported metadata action")
	}
	return nil
}

// Manage is only called by the local control API after active-call protection.
func (r *ConversationRegistry) Manage(ctx context.Context, id string, change MetadataChange) (Conversation, error) {
	var item Conversation
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		record, found := state.Items[id]
		if !found {
			return false, ErrConversationNotFound
		}
		if change.Action == "delete" {
			if record.TrashedAt == nil {
				return false, errors.New("move the conversation to the recycle bin before permanent removal")
			}
			item = cloneConversation(record.Conversation)
			now := time.Now().UTC()
			record.DeletedAt, record.TerminatedAt = &now, &now
			record.TaskIDs, record.WorkspaceIDs = nil, nil
			record.State = ConversationState{BindingRevision: record.State.BindingRevision + 1, UpdatedAt: now}
			state.Items[id] = record // Durable source tombstone prevents reconnect/replay resurrection.
			return true, nil
		}
		if err := ApplyManagement(&record.Management, &record.Title, change, time.Now().UTC()); err != nil {
			return false, err
		}
		if record.DeletedAt != nil {
			return false, ErrConversationNotFound
		}
		if change.Action == "rename" {
			record.TitleSource = "manual"
		}
		record.UpdatedAt = time.Now().UTC()
		state.Items[id] = record
		item = cloneConversation(record.Conversation)
		return true, nil
	})
	return item, err
}
