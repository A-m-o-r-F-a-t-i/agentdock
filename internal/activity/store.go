package activity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

const defaultSegmentBytes = 8 << 20
const defaultSegments = 8

type Options struct {
	SegmentBytes int64
	Segments     int
}
type Store struct {
	root     string
	options  Options
	mu       sync.Mutex
	changed  chan struct{}
	redactor Redactor
}
type sequenceState struct {
	Seq           uint64 `json:"seq"`
	PrunedThrough uint64 `json:"pruned_through"`
}

func New(root string, options Options, secrets ...string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("activity root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	if err = regularPath(abs, true); err != nil {
		return nil, err
	}
	if options.SegmentBytes <= 0 {
		options.SegmentBytes = defaultSegmentBytes
	}
	if options.Segments <= 0 {
		options.Segments = defaultSegments
	}
	if options.Segments > 64 {
		return nil, errors.New("activity segment count exceeds 64")
	}
	return &Store{root: abs, options: options, changed: make(chan struct{}), redactor: NewRedactor(secrets...)}, nil
}

func (s *Store) Changed() <-chan struct{} { s.mu.Lock(); defer s.mu.Unlock(); return s.changed }

func (s *Store) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	release, err := filelock.Acquire(ctx, filepath.Join(s.root, ".activity.lock"))
	cancel()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	return func() { release(); s.mu.Unlock() }, nil
}

func (s *Store) Append(ctx context.Context, e Event) (Event, error) {
	if err := e.Binding.Validate(); err != nil {
		return Event{}, err
	}
	if !validKind(e.Kind) {
		return Event{}, errors.New("invalid activity kind")
	}
	e = s.redactor.Event(e)
	if len(e.ToolName) > 160 || len(e.SessionID) > 80 || len(e.Runtime) > 32 || len(e.Status) > 32 {
		return Event{}, errors.New("activity metadata is too long")
	}
	release, err := s.lock(ctx)
	if err != nil {
		return Event{}, err
	}
	defer release()
	files, err := s.segments()
	if err != nil {
		return Event{}, err
	}
	state, err := s.state(files)
	if err != nil {
		return Event{}, err
	}
	if state.Seq == ^uint64(0) {
		return Event{}, errors.New("activity sequence exhausted")
	}
	state.Seq++
	e.SchemaVersion, e.Seq, e.CreatedAt = SchemaVersion, state.Seq, time.Now().UTC()
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Event{}, err
	}
	e.EventID = "evt_" + hex.EncodeToString(id[:])
	data, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	data = append(data, '\n')
	if len(data) > MaxEventBytes {
		return Event{}, errors.New("activity event exceeds size limit")
	}
	path := filepath.Join(s.root, fmt.Sprintf("%020d.jsonl", e.Seq))
	if len(files) > 0 {
		last := files[len(files)-1]
		info, statErr := os.Stat(last)
		if statErr != nil {
			return Event{}, statErr
		}
		if info.Size()+int64(len(data)) <= s.options.SegmentBytes {
			path = last
			if err = repairTail(path); err != nil {
				return Event{}, err
			}
		}
	}
	// Reserve the sequence durably before appending. A crash may leave a gap, never a duplicate.
	if err = s.saveState(state); err != nil {
		return Event{}, err
	}
	if err = regularPath(path, false); err != nil {
		return Event{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return Event{}, err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return Event{}, err
	}
	close(s.changed)
	s.changed = make(chan struct{})
	files, err = s.segments()
	if err != nil {
		return e, err
	}
	if len(files) > s.options.Segments {
		count := len(files) - s.options.Segments
		start, _ := segmentSequence(files[count])
		state.PrunedThrough = start - 1
		if err = s.saveState(state); err != nil {
			return e, err
		}
		for _, old := range files[:count] {
			if err = os.Remove(old); err != nil {
				return e, err
			}
		}
	}
	return e, nil
}

func (s *Store) Query(ctx context.Context, q Query) (Page, error) {
	page := Page{Events: []Event{}, NextSeq: q.After}
	if q.Limit <= 0 || q.Limit > MaxQueryEvents {
		q.Limit = MaxQueryEvents
	}
	if err := (Binding{TaskID: q.TaskID, ThreadID: q.ThreadID}).Validate(); err != nil {
		return page, err
	}
	release, err := s.lock(ctx)
	if err != nil {
		return page, err
	}
	defer release()
	files, err := s.segments()
	if err != nil {
		return page, err
	}
	state, err := s.state(files)
	if err != nil {
		return page, err
	}
	page.LatestSeq, page.PrunedThrough = state.Seq, state.PrunedThrough
	page.Gap = q.After > 0 && q.After < state.PrunedThrough
	if q.After >= state.Seq {
		return page, nil
	}
	for i, path := range files {
		if i+1 < len(files) {
			next, _ := segmentSequence(files[i+1])
			if next <= q.After {
				continue
			}
		}
		if err = ctx.Err(); err != nil {
			return page, err
		}
		warning := false
		err = scanEvents(path, func(e Event) bool {
			if e.Seq <= q.After || e.Seq <= page.PrunedThrough {
				return true
			}
			if q.TaskID != "" && e.TaskID != q.TaskID || q.ThreadID != "" && e.ThreadID != q.ThreadID {
				page.NextSeq = e.Seq
				return true
			}
			if len(page.Events) == q.Limit {
				page.HasMore = true
				return false
			}
			page.Events = append(page.Events, e)
			page.NextSeq = e.Seq
			return true
		}, &warning)
		if err != nil {
			return page, err
		}
		if warning && len(page.Warnings) < 8 {
			page.Warnings = append(page.Warnings, "Skipped incomplete or damaged activity records in "+filepath.Base(path))
		}
		if page.HasMore {
			break
		}
	}
	if !page.HasMore {
		page.NextSeq = state.Seq
	}
	return page, nil
}

// Cleanup removes whole expired segments and advances the replay retention floor.
// The current segment is kept so concurrent writers retain a stable append target.
func (s *Store) Cleanup(ctx context.Context, before time.Time) (int, error) {
	if before.IsZero() || before.After(time.Now()) {
		return 0, errors.New("cleanup cutoff must be in the past")
	}
	release, err := s.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	files, err := s.segments()
	if err != nil {
		return 0, err
	}
	state, err := s.state(files)
	if err != nil {
		return 0, err
	}
	count := 0
	for i := 0; i+1 < len(files); i++ {
		info, err := os.Stat(files[i])
		if err != nil {
			return count, err
		}
		if !info.ModTime().Before(before) {
			break
		}
		next, _ := segmentSequence(files[i+1])
		state.PrunedThrough = next - 1
		if err = s.saveState(state); err != nil {
			return count, err
		}
		if err = os.Remove(files[i]); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Store) segments() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, entry := range entries {
		if _, ok := segmentSequence(entry.Name()); ok {
			path := filepath.Join(s.root, entry.Name())
			if err := regularPath(path, false); err != nil {
				return nil, err
			}
			files = append(files, path)
		}
	}
	if len(files) > 128 {
		return nil, errors.New("activity segment inventory exceeds safety limit")
	}
	sort.Strings(files)
	return files, nil
}

func segmentSequence(path string) (uint64, bool) {
	name := filepath.Base(path)
	if len(name) != 26 || !strings.HasSuffix(name, ".jsonl") {
		return 0, false
	}
	n, err := strconv.ParseUint(name[:20], 10, 64)
	return n, err == nil && n > 0
}

func (s *Store) state(files []string) (sequenceState, error) {
	var state sequenceState
	path := filepath.Join(s.root, "sequence.json")
	if err := regularPath(path, false); err != nil {
		return state, err
	}
	f, err := os.Open(path)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 4097))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return state, errors.Join(readErr, closeErr)
		}
		if len(data) > 4096 || json.Unmarshal(data, &state) != nil {
			return state, errors.New("activity sequence state is damaged; preserve it before repair")
		}
	} else if !os.IsNotExist(err) {
		return state, err
	}
	if len(files) > 0 {
		first, _ := segmentSequence(files[0])
		if first > 1 && state.PrunedThrough < first-1 {
			state.PrunedThrough = first - 1
		}
		// A missing counter can be reconstructed from the final segment without reusing persisted ids.
		if os.IsNotExist(err) {
			if err := scanEvents(files[len(files)-1], func(e Event) bool {
				if e.Seq > state.Seq {
					state.Seq = e.Seq
				}
				return true
			}, nil); err != nil {
				return state, err
			}
		}
	}
	return state, nil
}

func (s *Store) saveState(state sequenceState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.root, "sequence.json"), data, 0600)
}

func regularPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !directory {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("activity storage requires regular files and directories")
	}
	return nil
}

func scanEvents(path string, visit func(Event) bool, warning *bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, MaxEventBytes+1)
	for {
		line, readErr := reader.ReadSlice('\n')
		if len(line) > MaxEventBytes {
			return errors.New("activity record exceeds read limit")
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var e Event
			if json.Unmarshal(line, &e) != nil || e.SchemaVersion != SchemaVersion || e.Seq == 0 || e.Binding.Validate() != nil || !validKind(e.Kind) {
				if warning != nil {
					*warning = true
				}
			} else if !visit(e) {
				return nil
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func repairTail(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	start := max(int64(0), info.Size()-MaxEventBytes-1)
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxEventBytes+1))
	if err != nil {
		return err
	}
	if data[len(data)-1] == '\n' {
		return nil
	}
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 && start > 0 {
		return errors.New("activity tail exceeds recovery limit")
	}
	var e Event
	if json.Unmarshal(data[last+1:], &e) == nil && e.SchemaVersion == SchemaVersion && e.Seq > 0 {
		if _, err = f.Seek(0, io.SeekEnd); err == nil {
			_, err = f.Write([]byte{'\n'})
		}
	} else {
		err = f.Truncate(start + int64(last+1))
	}
	if err != nil {
		return err
	}
	return f.Sync()
}
