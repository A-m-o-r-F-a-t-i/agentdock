package activity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxConversationBytes = 16 << 20

type conversationSnapshot struct {
	state  *conversationState
	digest [sha256.Size]byte
	exists bool
}

// Verify actual content under the cross-process lock. Size, timestamp and
// identity alone cannot detect every in-place external rewrite. Warm reads
// reuse a bounded buffer and never allocate another complete JSON document.
func (r *ConversationRegistry) readSnapshot(ctx context.Context, path string) (*conversationSnapshot, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return &conversationSnapshot{state: &conversationState{SchemaVersion: 1, Items: map[string]conversationRecord{}, sources: map[string]string{}}}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConversationBytes {
		return nil, errors.New("invalid conversation registry file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("conversation registry changed while opening")
	}
	if r.readBuffer == nil {
		r.readBuffer = make([]byte, 32<<10)
	}
	hash := sha256.New()
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(r.readBuffer)
		total += n
		if total > maxConversationBytes {
			return nil, errors.New("conversation registry is too large")
		}
		_, _ = hash.Write(r.readBuffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	if r.cached != nil && r.cached.exists && r.cached.digest == digest {
		return r.cached, nil
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConversationBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConversationBytes {
		return nil, errors.New("conversation registry is too large")
	}
	if sha256.Sum256(data) != digest {
		return nil, errors.New("conversation registry changed during verification")
	}
	state := &conversationState{}
	r.decodeCount.Add(1)
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("read conversation registry: %w", err)
	}
	if err := indexConversationState(state); err != nil {
		return nil, err
	}
	return &conversationSnapshot{state: state, digest: digest, exists: true}, nil
}

func indexConversationState(state *conversationState) error {
	if state.SchemaVersion != 1 || state.Items == nil || len(state.Items) > 20000 {
		return errors.New("unsupported conversation registry schema or capacity")
	}
	state.sources = make(map[string]string, len(state.Items))
	for id, item := range state.Items {
		if !conversationIdentifier.MatchString(id) || id != item.ID {
			return errors.New("invalid conversation registry identity")
		}
		if item.SourceKey != "" {
			if previous, exists := state.sources[item.SourceKey]; exists && previous != id {
				return errors.New("duplicate conversation source mapping")
			}
			state.sources[item.SourceKey] = id
		}
	}
	return nil
}
