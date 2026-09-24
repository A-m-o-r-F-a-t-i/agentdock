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
	"sync/atomic"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

const defaultSegmentBytes = 8 << 20
const defaultSegments = 8
const appendBatchWindow = 250 * time.Microsecond
const maxAppendBatch = 64

type Options struct {
	SegmentBytes int64
	Segments     int
}
type Store struct {
	projection       *callProjection
	root             string
	options          Options
	mu               sync.Mutex
	payloadMu        sync.Mutex
	appendQueueMu    sync.Mutex
	appendQueue      []*appendRequest
	appendWorker     bool
	changed          chan struct{}
	redactor         Redactor
	payloadDirectory os.FileInfo
}
type sequenceState struct {
	Seq           uint64 `json:"seq"`
	PrunedThrough uint64 `json:"pruned_through"`
}
type appendRequest struct {
	ctx       context.Context
	event     Event
	done      chan appendResult
	mu        sync.Mutex
	cancelled bool
	committed bool
}
type appendResult struct {
	event Event
	err   error
}
type preparedAppend struct {
	request *appendRequest
	event   Event
	data    []byte
	path    string
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
	return s.lockResource(ctx, &s.mu, ".activity.lock")
}

func (s *Store) lockPayload(ctx context.Context) (func(), error) {
	return s.lockResource(ctx, &s.payloadMu, ".payload.lock")
}

func (s *Store) lockResource(ctx context.Context, mutex *sync.Mutex, name string) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !mutex.TryLock() {
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for !mutex.TryLock() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-tick.C:
			}
		}
	}
	release, err := filelock.Acquire(ctx, filepath.Join(s.root, name))
	if err != nil {
		mutex.Unlock()
		return nil, err
	}
	return func() { release(); mutex.Unlock() }, nil
}

func (s *Store) Append(ctx context.Context, e Event) (Event, error) {
	var err error
	e, err = s.prepareAppend(ctx, e)
	if err != nil {
		return Event{}, err
	}
	request := &appendRequest{ctx: ctx, event: e, done: make(chan appendResult, 1)}
	s.appendQueueMu.Lock()
	s.appendQueue = append(s.appendQueue, request)
	if !s.appendWorker {
		s.appendWorker = true
		go s.drainAppendQueue()
	}
	s.appendQueueMu.Unlock()
	select {
	case result := <-request.done:
		return result.event, result.err
	case <-ctx.Done():
		request.mu.Lock()
		if request.committed {
			request.mu.Unlock()
			result := <-request.done
			return result.event, result.err
		}
		request.cancelled = true
		request.mu.Unlock()
		return Event{}, ctx.Err()
	}
}

// AppendBatch durably appends a small, already ordered lifecycle group with a
// single journal synchronization. The cross-process lock and projection update
// cover the whole group, so readers can never observe reordered sequence IDs.
func (s *Store) AppendBatch(ctx context.Context, events []Event) ([]Event, error) {
	if len(events) == 0 {
		return []Event{}, nil
	}
	if len(events) > maxAppendBatch {
		return nil, fmt.Errorf("activity append batch exceeds %d events", maxAppendBatch)
	}
	requests := make([]*appendRequest, len(events))
	for index, event := range events {
		prepared, err := s.prepareAppend(ctx, event)
		if err != nil {
			return nil, err
		}
		requests[index] = &appendRequest{ctx: ctx, event: prepared}
	}
	release, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		release()
		return nil, err
	}
	results := s.appendBatchLocked(requests)
	release()
	appended := make([]Event, len(requests))
	var resultErr error
	for index, request := range requests {
		result, ok := results[request]
		if !ok {
			result.err = errors.New("activity append batch did not produce a result")
		}
		appended[index] = result.event
		resultErr = errors.Join(resultErr, result.err)
	}
	return appended, resultErr
}

func (s *Store) prepareAppend(ctx context.Context, e Event) (Event, error) {
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
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	return e, nil
}

func (request *appendRequest) commit() error {
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.cancelled {
		return request.ctx.Err()
	}
	if err := request.ctx.Err(); err != nil {
		request.cancelled = true
		return err
	}
	request.committed = true
	return nil
}

func appendBatchLockContext(requests []*appendRequest) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var remaining atomic.Int32
	remaining.Store(int32(len(requests)))
	stops := make([]func() bool, 0, len(requests))
	for _, request := range requests {
		stop := context.AfterFunc(request.ctx, func() {
			if remaining.Add(-1) == 0 {
				cancel()
			}
		})
		stops = append(stops, stop)
	}
	cleanup := func() {
		cancel()
		for _, stop := range stops {
			stop()
		}
	}
	return ctx, cleanup
}

func (s *Store) drainAppendQueue() {
	for {
		time.Sleep(appendBatchWindow)
		s.appendQueueMu.Lock()
		if len(s.appendQueue) == 0 {
			s.appendWorker = false
			s.appendQueueMu.Unlock()
			return
		}
		count := min(len(s.appendQueue), maxAppendBatch)
		batch := append([]*appendRequest(nil), s.appendQueue[:count]...)
		clear(s.appendQueue[:count])
		s.appendQueue = s.appendQueue[count:]
		if len(s.appendQueue) == 0 {
			s.appendQueue = nil
		}
		s.appendQueueMu.Unlock()
		s.appendBatch(batch)
	}
}

func (s *Store) appendBatch(batch []*appendRequest) {
	results := make(map[*appendRequest]appendResult, len(batch))
	active := make([]*appendRequest, 0, len(batch))
	for _, request := range batch {
		if err := request.ctx.Err(); err != nil {
			results[request] = appendResult{err: err}
			continue
		}
		active = append(active, request)
	}
	if len(active) > 0 {
		lockCtx, cancel := appendBatchLockContext(active)
		release, err := s.lock(lockCtx)
		cancel()
		if err != nil {
			for _, request := range active {
				results[request] = appendResult{err: err}
			}
		} else {
			stillActive := active[:0]
			for _, request := range active {
				if err := request.commit(); err != nil {
					results[request] = appendResult{err: err}
					continue
				}
				stillActive = append(stillActive, request)
			}
			if len(stillActive) > 0 {
				for request, result := range s.appendBatchLocked(stillActive) {
					results[request] = result
				}
			}
			release()
		}
	}
	for _, request := range batch {
		result, ok := results[request]
		if !ok {
			result.err = errors.New("activity append batch did not produce a result")
		}
		request.done <- result
	}
}

// appendBatchLocked allocates sequence numbers while the cross-process journal
// lock is held, then makes the journal itself the durable source of truth. A
// caller only receives a sequence after the containing segment has been synced.
// The optional sequence state may remain ahead after an older interrupted
// writer, so those already-reserved gaps are still never reused.
func (s *Store) appendBatchLocked(requests []*appendRequest) map[*appendRequest]appendResult {
	results := make(map[*appendRequest]appendResult, len(requests))
	failAll := func(err error) map[*appendRequest]appendResult {
		for _, request := range requests {
			results[request] = appendResult{err: err}
		}
		return results
	}
	files, err := s.segments()
	if err != nil {
		return failAll(err)
	}
	state, err := s.state(files)
	if err != nil {
		return failAll(err)
	}
	if uint64(len(requests)) > ^uint64(0)-state.Seq {
		return failAll(errors.New("activity sequence exhausted"))
	}
	currentPath := ""
	var currentSize int64
	if len(files) > 0 {
		currentPath = files[len(files)-1]
		if err = repairTail(currentPath); err != nil {
			return failAll(err)
		}
		info, statErr := os.Stat(currentPath)
		if statErr != nil {
			return failAll(statErr)
		}
		currentSize = info.Size()
	}
	prepared := make([]preparedAppend, 0, len(requests))
	for _, request := range requests {
		state.Seq++
		event := request.event
		event.SchemaVersion, event.Seq, event.CreatedAt = SchemaVersion, state.Seq, time.Now().UTC()
		var id [12]byte
		if _, err = rand.Read(id[:]); err != nil {
			return failAll(err)
		}
		event.EventID = "evt_" + hex.EncodeToString(id[:])
		data, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return failAll(marshalErr)
		}
		data = append(data, '\n')
		if len(data) > MaxEventBytes {
			return failAll(errors.New("activity event exceeds size limit"))
		}
		if currentPath == "" || currentSize+int64(len(data)) > s.options.SegmentBytes {
			currentPath = filepath.Join(s.root, fmt.Sprintf("%020d.jsonl", event.Seq))
			currentSize = 0
			files = append(files, currentPath)
		}
		prepared = append(prepared, preparedAppend{request: request, event: event, data: data, path: currentPath})
		currentSize += int64(len(data))
	}
	succeeded := make([]preparedAppend, 0, len(prepared))
	for start := 0; start < len(prepared); {
		end := start + 1
		for end < len(prepared) && prepared[end].path == prepared[start].path {
			end++
		}
		path := prepared[start].path
		if err = regularPath(path, false); err == nil {
			var block bytes.Buffer
			for _, item := range prepared[start:end] {
				_, _ = block.Write(item.data)
			}
			var file *os.File
			file, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err == nil {
				written, writeErr := file.Write(block.Bytes())
				if writeErr == nil && written != block.Len() {
					writeErr = io.ErrShortWrite
				}
				syncErr := file.Sync()
				closeErr := file.Close()
				err = errors.Join(writeErr, syncErr, closeErr)
			}
		}
		if err != nil {
			for _, item := range prepared[start:] {
				results[item.request] = appendResult{err: err}
			}
			break
		}
		for _, item := range prepared[start:end] {
			s.projectAppendedLocked(item.event)
			results[item.request] = appendResult{event: item.event}
			succeeded = append(succeeded, item)
		}
		start = end
	}
	if len(succeeded) == 0 {
		return results
	}
	close(s.changed)
	s.changed = make(chan struct{})
	files, err = s.segments()
	if err == nil && len(files) > s.options.Segments {
		count := len(files) - s.options.Segments
		start, _ := segmentSequence(files[count])
		state.PrunedThrough = start - 1
		if err = s.saveState(state); err == nil {
			for _, old := range files[:count] {
				if removeErr := os.Remove(old); removeErr != nil {
					err = removeErr
					break
				}
			}
		}
	}
	if err != nil {
		for _, item := range succeeded {
			result := results[item.request]
			result.err = err
			results[item.request] = result
		}
	}
	return results
}

func (s *Store) Query(ctx context.Context, q Query) (Page, error) {
	page := Page{Events: []Event{}, NextSeq: q.After}
	if q.Limit <= 0 || q.Limit > MaxQueryEvents {
		q.Limit = MaxQueryEvents
	}
	if err := (Binding{TaskID: q.TaskID, ThreadID: q.ThreadID, ConversationID: q.ConversationID, CallID: q.CallID}).Validate(); err != nil {
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
			if q.TaskID != "" && e.TaskID != q.TaskID || q.ThreadID != "" && e.ThreadID != q.ThreadID || q.ConversationID != "" && e.ConversationID != q.ConversationID || q.CallID != "" && e.CallID != q.CallID {
				page.NextSeq = e.Seq
				return true
			}
			if q.MilestonesOnly && !(strings.HasPrefix(e.Kind, "task.") || strings.HasPrefix(e.Kind, "step.") || strings.HasPrefix(e.Kind, "review.") || strings.HasPrefix(e.Kind, "thread.")) {
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
		// The append-only journal is authoritative for committed events. Read the
		// newest valid record from the tail even when sequence.json exists: normal
		// appends deliberately avoid a second atomic fsync for that derived value.
		// Walk older segments only when a damaged final segment contains no valid
		// record at all.
		for i := len(files) - 1; i >= 0; i-- {
			seq, found, tailErr := latestSegmentSequence(files[i])
			if tailErr != nil {
				return state, tailErr
			}
			if !found {
				continue
			}
			if seq > state.Seq {
				state.Seq = seq
			}
			break
		}
	}
	return state, nil
}

// latestSegmentSequence reads the bounded final record in the common case and
// falls back to a complete segment scan only when newline-terminated damage
// follows the last valid event. Segments are size bounded by Store options.
func latestSegmentSequence(path string) (uint64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, false, err
	}
	if info.Size() == 0 {
		return 0, false, f.Close()
	}
	start := max(int64(0), info.Size()-int64(MaxEventBytes+1))
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		_ = f.Close()
		return 0, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, int64(MaxEventBytes+1)))
	closeErr := f.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return 0, false, err
	}
	if start > 0 {
		if boundary := bytes.IndexByte(data, '\n'); boundary >= 0 {
			data = data[boundary+1:]
		} else {
			data = nil
		}
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || len(line) > MaxEventBytes {
			continue
		}
		var event Event
		if json.Unmarshal(line, &event) == nil && event.SchemaVersion == SchemaVersion && event.Seq > 0 && event.Binding.Validate() == nil && validKind(event.Kind) {
			return event.Seq, true, nil
		}
	}
	var latest uint64
	if err = scanEvents(path, func(event Event) bool {
		if event.Seq > latest {
			latest = event.Seq
		}
		return true
	}, nil); err != nil {
		return 0, false, err
	}
	return latest, latest > 0, nil
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
		if len(line) > MaxEventBytes || errors.Is(readErr, bufio.ErrBufferFull) {
			// A damaged oversized line is skipped in bounded chunks. A later valid
			// record and other tasks remain readable; original bytes stay intact.
			if warning != nil {
				*warning = true
			}
			for errors.Is(readErr, bufio.ErrBufferFull) {
				_, readErr = reader.ReadSlice('\n')
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			continue
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
