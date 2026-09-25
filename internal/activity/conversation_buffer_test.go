package activity

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// This isolated experiment varies only read granularity. It never bypasses
// the cross-process lock or content verification, and is not a runtime option.
func TestConversationReadBufferExperiment(t *testing.T) {
	if os.Getenv("AGENTDOCK_RUN_REGISTRY_PERF") != "1" {
		t.Skip("opt-in local buffer experiment")
	}
	root := t.TempDir()
	writer, err := NewConversationRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSource(context.Background(), Source{Principal: "performance-owner", HostConversationID: "measured"})
	target, err := writer.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.state(ctx, func(state *conversationState) (bool, error) {
		for index := 1; index < 10000; index++ {
			id := fmt.Sprintf("conv_%032x", index)
			state.Items[id] = conversationRecord{Conversation: Conversation{ID: id, Title: fmt.Sprintf("Synthetic conversation %d", index), TitleSource: "host", CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC(), State: ConversationState{BindingRevision: 1}}, OwnerKey: "owner", SourceKey: fmt.Sprintf("source-%d", index)}
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		sizes := []int{32 << 10, 256 << 10, 1 << 20}
		if round%2 != 0 {
			sizes = []int{1 << 20, 256 << 10, 32 << 10}
		}
		for _, size := range sizes {
			reader, err := NewConversationRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			reader.readBuffer = make([]byte, size)
			if _, err = reader.Get(ctx, target.ID); err != nil {
				t.Fatal(err)
			}
			values := make([]time.Duration, 51)
			for index := range values {
				started := time.Now()
				if _, err = reader.Get(ctx, target.ID); err != nil {
					t.Fatal(err)
				}
				values[index] = time.Since(started)
			}
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			t.Logf("round=%d bytes=%d records=10000 samples=51 median_us=%.3f p95_us=%.3f decodes=%d", round, size, float64(values[25].Nanoseconds())/1000, float64(values[48].Nanoseconds())/1000, reader.decodeCount.Load())
		}
	}
}
