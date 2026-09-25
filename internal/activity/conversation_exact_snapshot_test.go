package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExactSnapshotDetectsPrefixMiddleSuffixAndLengthChanges(t *testing.T) {
	for _, variant := range []string{"prefix", "middle", "suffix", "append", "truncate"} {
		t.Run(variant, func(t *testing.T) {
			r, err := NewConversationRegistry(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithSource(context.Background(), Source{Principal: "owner", HostConversationID: "exact"})
			item, err := r.Resolve(ctx)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(r.root, "conversations.json")
			old, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			// Use a small test buffer to cross many actual chunk boundaries.
			r.readBuffer = make([]byte, 31)
			changed := bytes.Clone(old)
			switch variant {
			case "prefix":
				changed[0] = '['
			case "middle":
				changed[len(changed)/2] = 0
			case "suffix":
				changed[len(changed)-2] = '['
			case "append":
				changed = append(changed, ' ')
			case "truncate":
				changed = changed[:len(changed)/2]
			}
			if err = os.WriteFile(file, changed, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = r.Get(ctx, item.ID)
			if variant == "append" {
				if err != nil || r.cached == nil || !bytes.Equal(r.cached.serialized, changed) {
					t.Fatalf("valid length change not loaded: %v", err)
				}
			} else if err == nil || r.cached != nil {
				t.Fatal("changed malformed bytes served stale authority")
			}
			if err = os.WriteFile(file, old, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = r.Get(ctx, item.ID); err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if json.Unmarshal(r.cached.serialized, &decoded) != nil {
				t.Fatal("cache no longer holds immutable serialized state")
			}
		})
	}
}
