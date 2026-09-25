package activity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
)

const maxConversationBytes = 16 << 20
const conversationReadChunk = 256 << 10

type conversationSnapshot struct {
	state      *conversationState
	serialized []byte // One bounded immutable encoded snapshot, never a warm-read allocation.
	exists     bool
}

// Verify actual content under the cross-process lock. Size, timestamp and
// identity alone cannot detect every in-place external rewrite. Warm reads
// compare every byte against one immutable version with a reusable buffer.
// Changed/cold reads retain the double-read integrity check before parsing.
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
		r.readBuffer = make([]byte, conversationReadChunk)
	}
	cached := r.cached
	matching := cached != nil && cached.exists && cached.serialized != nil
	var fingerprint hash.Hash
	if !matching {
		fingerprint = sha256.New()
	}
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(r.readBuffer)
		previous := total
		total += n
		if total > maxConversationBytes {
			return nil, errors.New("conversation registry is too large")
		}
		if matching && (total > len(cached.serialized) || !bytes.Equal(r.readBuffer[:n], cached.serialized[previous:total])) {
			matching = false
			fingerprint = sha256.New()
			// The preceding bytes were compared exactly, so the cached prefix
			// is the same prefix actually read from this file handle.
			_, _ = fingerprint.Write(cached.serialized[:previous])
		}
		if !matching {
			_, _ = fingerprint.Write(r.readBuffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if matching && total == len(cached.serialized) {
		return cached, nil
	}
	if matching {
		fingerprint = sha256.New()
		_, _ = fingerprint.Write(cached.serialized[:total])
	}
	var digest [sha256.Size]byte
	fingerprint.Sum(digest[:0])
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
	return &conversationSnapshot{state: state, serialized: data, exists: true}, nil
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
