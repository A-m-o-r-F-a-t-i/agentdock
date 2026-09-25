package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

// The same fixture can run unchanged at the audited commit and the candidate.
// It measures real Get operations, including locking and external validation.
func TestConversationPerformance(t *testing.T) {
	if os.Getenv("AGENTDOCK_RUN_REGISTRY_PERF") != "1" {
		t.Skip("opt-in same-machine registry measurement")
	}
	type measurement struct {
		Records               int     `json:"records"`
		Phase                 string  `json:"phase"`
		Samples               int     `json:"samples"`
		MedianUS              float64 `json:"median_us"`
		P95US                 float64 `json:"p95_us"`
		AllocatedBytesPerRead uint64  `json:"allocated_bytes_per_read"`
		AllocationsPerRead    uint64  `json:"allocations_per_read"`
	}
	report := struct {
		Label     string        `json:"label"`
		Toolchain string        `json:"toolchain"`
		Platform  string        `json:"platform"`
		CPUs      int           `json:"cpus"`
		Rows      []measurement `json:"rows"`
	}{Label: os.Getenv("AGENTDOCK_REGISTRY_PERF_LABEL"), Toolchain: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU()}
	for _, count := range []int{100, 1000, 10000, 20000} {
		root := t.TempDir()
		r, err := NewConversationRegistry(root)
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithSource(context.Background(), Source{Principal: "performance-owner", HostConversationID: "measured"})
		target, err := r.Resolve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = r.state(ctx, func(state *conversationState) (bool, error) {
			for i := 1; i < count; i++ {
				id := fmt.Sprintf("conv_%032x", i)
				state.Items[id] = conversationRecord{Conversation: Conversation{ID: id, Title: fmt.Sprintf("Synthetic conversation %d", i), TitleSource: "host", CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC(), State: ConversationState{BindingRevision: 1}}, OwnerKey: "owner", SourceKey: fmt.Sprintf("source-%d", i)}
			}
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, phase := range []string{"cold", "warm"} {
			reader, err := NewConversationRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			samples := 11
			if phase == "warm" {
				samples = 51
				if _, err = reader.Get(ctx, target.ID); err != nil {
					t.Fatal(err)
				}
			}
			elapsed := make([]int64, 0, samples)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for range samples {
				if phase == "cold" {
					reader, err = NewConversationRegistry(root)
					if err != nil {
						t.Fatal(err)
					}
				}
				started := time.Now()
				if _, err = reader.Get(ctx, target.ID); err != nil {
					t.Fatal(err)
				}
				elapsed = append(elapsed, time.Since(started).Nanoseconds())
			}
			runtime.ReadMemStats(&after)
			sort.Slice(elapsed, func(i, j int) bool { return elapsed[i] < elapsed[j] })
			row := measurement{Records: count, Phase: phase, Samples: samples, MedianUS: float64(elapsed[(samples-1)/2]) / 1000, P95US: float64(elapsed[(samples*95+99)/100-1]) / 1000, AllocatedBytesPerRead: (after.TotalAlloc - before.TotalAlloc) / uint64(samples), AllocationsPerRead: (after.Mallocs - before.Mallocs) / uint64(samples)}
			report.Rows = append(report.Rows, row)
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if path := os.Getenv("AGENTDOCK_REGISTRY_PERF_OUTPUT"); path != "" {
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
