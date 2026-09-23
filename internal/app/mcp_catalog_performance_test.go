package app

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/config"
)

func TestMCPDiscoveryEndToEndPerformance(t *testing.T) {
	if os.Getenv("AGENTDOCK_MCP_PERF") != "1" {
		t.Skip("opt-in isolated full discovery-chain timing")
	}
	setUserHomeForTest(t, t.TempDir())
	fixture := newCatalogFixture(t, 200)
	type sample struct {
		Mode       string  `json:"mode"`
		Index      int     `json:"index"`
		ContextMS  float64 `json:"context_ms"`
		ListMS     float64 `json:"list_ms"`
		InspectMS  float64 `json:"inspect_ms"`
		BusinessMS float64 `json:"business_ms"`
		TotalMS    float64 `json:"total_ms"`
		Bytes      int     `json:"response_bytes"`
		Pages      int32   `json:"upstream_list_pages"`
		Error      string  `json:"error,omitempty"`
	}
	rows := []sample{}
	newRuntime := func() *Runtime {
		cfg := config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()}
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		r, err := newUnrestrictedTestRuntime(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Call(t.Context(), "mcp_manage", map[string]any{"action": "add", "name": "catalog", "description": "synthetic end-to-end fixture", "transport": "streamable_http", "url": fixture.server.URL}); err != nil {
			r.Close()
			t.Fatal(err)
		}
		return r
	}
	measure := func(r *Runtime, mode string, index int) {
		row := sample{Mode: mode, Index: index}
		before := fixture.lists.Load()
		start := time.Now()
		steps := []struct {
			name    string
			args    map[string]any
			elapsed *float64
		}{
			{"agentdock_context", map[string]any{"workdir": r.cfg.AgentDockDefaultDir}, &row.ContextMS},
			{"mcp_tool_list", map[string]any{"server": "catalog"}, &row.ListMS},
			{"mcp_tool_inspect", map[string]any{"names": []string{"catalog:*"}}, &row.InspectMS},
			{"mcp_tool_call", map[string]any{"name": "catalog:tool_000", "arguments": map[string]any{"text": "ok"}}, &row.BusinessMS},
		}
		for _, step := range steps {
			started := time.Now()
			result, err := r.Call(t.Context(), step.name, step.args)
			if err != nil {
				row.Error = err.Error()
				break
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				row.Error = err.Error()
				break
			}
			row.Bytes += len(encoded)
			*step.elapsed = float64(time.Since(started)) / float64(time.Millisecond)
		}
		row.TotalMS = float64(time.Since(start)) / float64(time.Millisecond)
		row.Pages = fixture.lists.Load() - before
		rows = append(rows, row)
		if row.Error != "" {
			t.Error(row.Error)
		}
		want := int32(0)
		if mode == "cold" {
			want = 3
		}
		if row.Pages != want {
			t.Errorf("%s discovery pages=%d, want %d", mode, row.Pages, want)
		}
	}
	for i := 0; i < 20; i++ {
		r := newRuntime()
		measure(r, "cold", i)
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	r := newRuntime()
	defer r.Close()
	measure(r, "cold", 20)
	for i := 0; i < 100; i++ {
		measure(r, "hot", i)
	}
	summary := []map[string]any{}
	for _, mode := range []string{"cold", "hot"} {
		values := []float64{}
		list := []float64{}
		inspect := []float64{}
		failures := 0
		for _, row := range rows {
			if row.Mode == mode {
				values = append(values, row.TotalMS)
				list = append(list, row.ListMS)
				inspect = append(inspect, row.InspectMS)
				if row.Error != "" {
					failures++
				}
			}
		}
		for _, set := range [][]float64{values, list, inspect} {
			sort.Float64s(set)
		}
		index := int(math.Ceil(.95*float64(len(values)))) - 1
		item := map[string]any{"mode": mode, "count": len(values), "failures": failures, "p50_ms": values[int(math.Ceil(.5*float64(len(values))))-1], "p95_ms": values[index], "max_ms": values[len(values)-1], "list_p95_ms": list[index], "inspect_full_200_p95_ms": inspect[index]}
		summary = append(summary, item)
		t.Logf("%v", item)
		if mode == "hot" && (list[index] > 200 || inspect[index] > 200) {
			t.Errorf("hot cached directory/schema root latency exceeds 200 ms: list=%.3f inspect=%.3f", list[index], inspect[index])
		}
	}
	report := map[string]any{"boundary": "Normal Runtime.Call + JSON encoding for context -> full list -> full 200-schema inspect -> first business call. Includes root observation, not external ChatGPT network/rendering. All servers, schemas and data are synthetic.", "cold": "Fresh Runtime and new MCP connection; OS file cache not flushed. Constructor/setup excluded and not reported as process startup.", "percentile": "nearest-rank", "summary": summary, "samples": rows}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("AGENTDOCK_MCP_PERF_OUTPUT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
