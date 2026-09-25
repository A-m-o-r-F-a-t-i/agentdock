package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
	pluginregistry "github.com/uvwt/agentdock/internal/plugin"
	"github.com/uvwt/agentdock/internal/snapshot"
)

type contextPerfPlugin struct {
	Skills  int  `json:"skills"`
	Servers int  `json:"servers"`
	Heavy   bool `json:"heavy"`
}

type contextPerfSample struct {
	Fixture      string         `json:"fixture"`
	Mode         string         `json:"mode"`
	Round        int            `json:"round"`
	Request      int            `json:"request"`
	Milliseconds float64        `json:"milliseconds"`
	Bytes        int            `json:"bytes"`
	Error        string         `json:"error,omitempty"`
	CallID       string         `json:"call_id,omitempty"`
	Diagnostics  map[string]any `json:"diagnostics,omitempty"`
}

type contextPerfGroup struct {
	Fixture          string                              `json:"fixture"`
	Mode             string                              `json:"mode"`
	Round            int                                 `json:"round"`
	Requests         int                                 `json:"requests"`
	WallMS           float64                             `json:"wall_ms"`
	PluginBefore     snapshot.Stats                      `json:"plugin_before"`
	PluginAfter      snapshot.Stats                      `json:"plugin_after"`
	SkillBefore      snapshot.Stats                      `json:"skill_before"`
	SkillAfter       snapshot.Stats                      `json:"skill_after"`
	ExternalRequests int32                               `json:"external_requests"`
	RegistryBefore   activity.ConversationReadStatistics `json:"registry_before"`
	RegistryAfter    activity.ConversationReadStatistics `json:"registry_after"`
}

type contextPerfSummary struct {
	Fixture  string  `json:"fixture"`
	Mode     string  `json:"mode"`
	Count    int     `json:"count"`
	Failures int     `json:"failures"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
	Max      float64 `json:"max_ms"`
}

type contextPerfReport struct {
	Version        int                            `json:"schema_version"`
	RecordedAt     string                         `json:"recorded_at"`
	GoVersion      string                         `json:"go_version"`
	OS             string                         `json:"os"`
	Arch           string                         `json:"arch"`
	CPU            int                            `json:"logical_cpus"`
	ColdDefinition string                         `json:"cold_definition"`
	Boundary       string                         `json:"measurement_boundary"`
	Percentile     string                         `json:"percentile"`
	Full           bool                           `json:"full_sampling"`
	Fixtures       map[string][]contextPerfPlugin `json:"fixtures"`
	Samples        []contextPerfSample            `json:"samples"`
	Groups         []contextPerfGroup             `json:"groups"`
	StartupMS      map[string][]float64           `json:"runtime_startup_ms"`
	Summary        []contextPerfSummary           `json:"summary"`
}

// Opt-in, synthetic data only. The actual-shape fixture freezes the 2026-09-23
// native plugin list (5 packages, 51 Skills, 5 MCPs, 3 Heavy), not private files.
// Each sample uses the normal observed root Call rather than Bridge LocalContext.
func TestContextPerformanceSamples(t *testing.T) {
	if os.Getenv("AGENTDOCK_CONTEXT_PERF") != "1" {
		t.Skip("opt-in isolated performance measurement")
	}
	full := os.Getenv("AGENTDOCK_CONTEXT_PERF_FULL") == "1"
	hotN, coldN, rounds := 10, 2, 2
	if full {
		hotN, coldN, rounds = 100, 20, 10
	}
	userHome := t.TempDir()
	setUserHomeForTest(t, userHome)
	writeCommonSkillForTest(t, filepath.Join(userHome, ".agents", "skills"), "common-perf", "common-perf", "Shared synthetic skill.")
	large := make([]contextPerfPlugin, 20)
	for i := range large {
		large[i] = contextPerfPlugin{Skills: 8, Servers: 1, Heavy: i%2 == 0}
	}
	report := contextPerfReport{
		Version: 1, RecordedAt: time.Now().UTC().Format(time.RFC3339), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPU: runtime.NumCPU(), Full: full,
		ColdDefinition: "Each cold sample reconstructs Runtime; its constructor-warmed plugin directory is invalidated before the first call. Rule, Skill and common caches are empty. Runtime construction is recorded separately. The OS file cache is NOT flushed.",
		Boundary:       "Runtime.Call(agentdock_context, workdir) through JSON encoding, including permissions, root observation, full rules, tasks and workspace selection. HTTP transport and ChatGPT rendering are not measured here; end-to-end MCP discovery is a separate report.",
		Percentile:     "nearest-rank: sorted[ceil(p*n)-1]; failures remain in sample count, not silently discarded",
		Fixtures:       map[string][]contextPerfPlugin{"none": {}, "actual-shape": {{3, 2, false}, {5, 1, true}, {0, 1, false}, {35, 1, true}, {8, 0, true}}, "large-160": large},
		Samples:        []contextPerfSample{}, Groups: []contextPerfGroup{}, StartupMS: map[string][]float64{},
	}
	var mu sync.Mutex
	defer func() {
		for _, fixture := range []string{"none", "actual-shape", "large-160"} {
			for _, mode := range []string{"cold", "hot", "concurrent-cold", "concurrent-hot"} {
				item := contextPerfSummary{Fixture: fixture, Mode: mode}
				values := []float64{}
				for _, sample := range report.Samples {
					if sample.Fixture != fixture || sample.Mode != mode {
						continue
					}
					item.Count++
					if sample.Error != "" {
						item.Failures++
					}
					values = append(values, sample.Milliseconds)
				}
				if len(values) == 0 {
					continue
				}
				sort.Float64s(values)
				item.P50 = values[int(math.Ceil(.50*float64(len(values))))-1]
				item.P95 = values[int(math.Ceil(.95*float64(len(values))))-1]
				item.Max = values[len(values)-1]
				report.Summary = append(report.Summary, item)
				t.Logf("%s %s n=%d failures=%d P50=%.3f P95=%.3f max=%.3f ms", fixture, mode, item.Count, item.Failures, item.P50, item.P95, item.Max)
				if item.Failures != 0 {
					t.Errorf("%s/%s contains %d failures", fixture, mode, item.Failures)
				}
				limit := 3000.0
				if mode == "hot" {
					limit = 500
					if item.P50 > 200 {
						t.Errorf("%s hot P50 %.3f > 200 ms", fixture, item.P50)
					}
				}
				if mode == "concurrent-hot" {
					limit = 1000
				}
				if full && item.P95 > limit {
					t.Errorf("%s/%s P95 %.3f > %.0f ms", fixture, mode, item.P95, limit)
				}
			}
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if path := os.Getenv("AGENTDOCK_CONTEXT_PERF_OUTPUT"); path != "" {
			if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	for _, fixture := range []string{"none", "actual-shape", "large-160"} {
		t.Run(fixture, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			workspace := filepath.Join(root, "project")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Synthetic rules\nKeep request identities separate.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var external atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				external.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			seedContextPerfPlugins(t, home, report.Fixtures[fixture], server.URL)
			cfg := config.Config{AgentDockDefaultDir: workspace, AgentDockHome: home}
			if err := cfg.Normalize(); err != nil {
				t.Fatal(err)
			}
			newRuntime := func() *Runtime {
				start := time.Now()
				rt, err := newUnrestrictedTestRuntime(cfg)
				elapsed := float64(time.Since(start)) / float64(time.Millisecond)
				if err != nil {
					t.Fatal(err)
				}
				report.StartupMS[fixture] = append(report.StartupMS[fixture], elapsed)
				return rt
			}
			measure := func(rt *Runtime, mode string, round, request int) {
				started := time.Now()
				ctx, cancel := context.WithTimeout(activity.WithSource(t.Context(), activity.Source{Principal: "context-perf", HostConversationID: fixture}), cfg.ContextBudget())
				defer cancel()
				result, err := rt.Call(ctx, "agentdock_context", map[string]any{"workdir": workspace})
				sample := contextPerfSample{Fixture: fixture, Mode: mode, Round: round, Request: request}
				if err != nil {
					sample.Error = err.Error()
				} else {
					data, encodeErr := json.Marshal(result)
					if encodeErr != nil {
						sample.Error = encodeErr.Error()
					}
					sample.Bytes = len(data)
					sample.CallID, _ = result["call_id"].(string)
					sample.Diagnostics, _ = result["context_diagnostics"].(map[string]any)
					if result["runtime"] == nil || result["instruction_files"] == nil || result["tasks"] == nil || sample.Diagnostics["complete"] != true {
						sample.Error = "normal root context is missing required fields or has incomplete diagnostics"
					}
					var decoded capabilityContext
					if err := remarshal(result, &decoded); err != nil {
						sample.Error = err.Error()
					}
					expectHeavy, expectSkills, expectMCP := 0, 0, 0
					for _, p := range report.Fixtures[fixture] {
						if p.Heavy {
							expectHeavy++
						} else {
							expectSkills += p.Skills
							expectMCP += p.Servers
						}
					}
					if len(decoded.Plugins) != expectHeavy || len(decoded.Skills) != expectSkills || len(decoded.DynamicMCP) != expectMCP {
						sample.Error = fmt.Sprintf("capability loss/leak: Heavy=%d/%d Skills=%d/%d MCP=%d/%d", len(decoded.Plugins), expectHeavy, len(decoded.Skills), expectSkills, len(decoded.DynamicMCP), expectMCP)
					}
				}
				sample.Milliseconds = float64(time.Since(started)) / float64(time.Millisecond)
				mu.Lock()
				report.Samples = append(report.Samples, sample)
				mu.Unlock()
			}
			clearCaches := func(rt *Runtime) {
				rt.pluginStore.Invalidate()
				rt.contextSnapshots.rules.Invalidate()
				rt.contextSnapshots.skills.Invalidate()
				rt.contextSnapshots.common.Invalidate()
			}
			group := func(rt *Runtime, mode string, round, n int, concurrent bool) {
				g := contextPerfGroup{Fixture: fixture, Mode: mode, Round: round, Requests: n, PluginBefore: rt.pluginStore.SnapshotStats(), SkillBefore: rt.contextSnapshots.skills.Stats()}
				g.RegistryBefore = rt.conversations.ReadStatistics()
				networkBefore := external.Load()
				started := time.Now()
				if concurrent {
					gate := make(chan struct{})
					var workers sync.WaitGroup
					for request := 0; request < n; request++ {
						workers.Add(1)
						go func(request int) { defer workers.Done(); <-gate; measure(rt, mode, round, request) }(request)
					}
					close(gate)
					workers.Wait()
				} else {
					for request := 0; request < n; request++ {
						measure(rt, mode, round, request)
					}
				}
				g.WallMS = float64(time.Since(started)) / float64(time.Millisecond)
				g.PluginAfter, g.SkillAfter = rt.pluginStore.SnapshotStats(), rt.contextSnapshots.skills.Stats()
				g.ExternalRequests = external.Load() - networkBefore
				g.RegistryAfter = rt.conversations.ReadStatistics()
				report.Groups = append(report.Groups, g)
				expectedBuilds := uint64(0)
				if strings.Contains(mode, "cold") {
					expectedBuilds = 1
				}
				if got := g.PluginAfter.Builds - g.PluginBefore.Builds; got != expectedBuilds {
					t.Errorf("%s/%s round %d plugin builds=%d, want %d", fixture, mode, round, got, expectedBuilds)
				}
				if g.ExternalRequests != 0 {
					t.Errorf("context contacted unselected service %d times", g.ExternalRequests)
				}
				if g.RegistryAfter.Decodes-g.RegistryBefore.Decodes > 1 {
					t.Errorf("%s/%s reparsed unchanged registry", fixture, mode)
				}
			}
			for i := 0; i < coldN; i++ {
				rt := newRuntime()
				clearCaches(rt)
				group(rt, "cold", i, 1, false)
				if err := rt.Close(); err != nil {
					t.Fatal(err)
				}
			}
			rt := newRuntime()
			defer rt.Close()
			if _, err := rt.Call(activity.WithSource(t.Context(), activity.Source{Principal: "context-perf", HostConversationID: fixture}), "agentdock_context", map[string]any{"workdir": workspace}); err != nil {
				t.Fatal(err)
			}
			group(rt, "hot", 0, hotN, false)
			for i := 0; i < rounds; i++ {
				clearCaches(rt)
				group(rt, "concurrent-cold", i, 10, true)
				group(rt, "concurrent-hot", i, 10, true)
			}
			if external.Load() != 0 {
				t.Errorf("unselected MCP contact count=%d", external.Load())
			}
		})
	}
}

func seedContextPerfPlugins(t *testing.T, home string, shapes []contextPerfPlugin, endpoint string) {
	t.Helper()
	for i, shape := range shapes {
		name := fmt.Sprintf("synthetic-%02d", i)
		root := filepath.Join(home, "plugins", name)
		meta := filepath.Join(root, pluginregistry.ManifestDirectory)
		if err := os.MkdirAll(meta, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := pluginregistry.Manifest{Schema: pluginregistry.ManifestSchema, Name: name, Description: "Synthetic capability metadata for isolated measurement.", Version: "1.0.0", Extensions: map[string]any{pluginregistry.ExtensionNamespace: map[string]any{"heavy": shape.Heavy}}}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(meta, pluginregistry.ManifestFilename), data, 0600); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < shape.Skills; j++ {
			skill := fmt.Sprintf("%s-skill-%03d", name, j)
			dir := filepath.Join(root, "skills", skill)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			document := fmt.Sprintf("---\nname: %s\ndescription: Synthetic measurement skill.\nversion: 1.0.0\n---\n\n# Skill\n%s", skill, strings.Repeat("Synthetic workflow content.\n", 20))
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if shape.Servers > 0 {
			servers := map[string]pluginregistry.MCPServer{}
			for j := 0; j < shape.Servers; j++ {
				servers[fmt.Sprintf("%s-mcp-%02d", name, j)] = pluginregistry.MCPServer{Type: "streamable-http", URL: endpoint}
			}
			data, err := json.Marshal(pluginregistry.MCPConfig{Schema: pluginregistry.MCPSchema, MCPServers: servers})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, pluginregistry.MCPFilename), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
