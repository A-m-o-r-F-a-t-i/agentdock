package desktopruntime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type memoryTailscaleCLI struct {
	node         tailscaleNode
	config       *tailscaleServeConfig
	commands     [][]string
	writeCount   int
	failWrite    int
	partialWrite bool
	afterWrite   func(*memoryTailscaleCLI)
}

func newMemoryTailscale(config *tailscaleServeConfig) *memoryTailscaleCLI {
	return &memoryTailscaleCLI{node: testTailscaleNode(), config: cloneTailscaleServe(config)}
}

func (fake *memoryTailscaleCLI) client() tailscaleClient { return tailscaleClient{run: fake.run} }

func (fake *memoryTailscaleCLI) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > tailscaleMutationTimeout {
		return nil, errors.New("unbounded command")
	}
	fake.commands = append(fake.commands, append([]string(nil), args...))
	if reflect.DeepEqual(args, []string{"status", "--json", "--peers=false"}) {
		caps := map[string]any{}
		for key := range fake.node.capabilities {
			caps[key] = nil
		}
		return json.Marshal(map[string]any{"BackendState": fake.node.BackendState, "Self": map[string]any{
			"ID": fake.node.ID, "HostName": fake.node.HostName, "DNSName": fake.node.DNSName,
			"Online": fake.node.Online, "KeyExpiry": fake.node.KeyExpiry, "CapMap": caps,
		}})
	}
	if reflect.DeepEqual(args, []string{"funnel", "status", "--json"}) {
		return json.Marshal(fake.config)
	}
	if len(args) != 6 || (args[0] != "funnel" && args[0] != "serve") || args[1] != "--bg" || args[2] != "--yes" || args[3] != "--https=443" || (args[4] != "--set-path=/" && args[4] != "--set-path=/mcp") {
		return nil, errors.New("unexpected or unsafe CLI command: " + strings.Join(args, " "))
	}
	fake.writeCount++
	fail := fake.writeCount == fake.failWrite
	if !fail || fake.partialWrite {
		var handler *tailscaleHTTPHandler
		if args[5] != "off" {
			handler = &tailscaleHTTPHandler{Proxy: args[5]}
		}
		fake.config = expectedTailscalePath(fake.config, fake.node.DNSName+":443", strings.TrimPrefix(args[4], "--set-path="), handler, args[0] == "funnel")
		if fake.afterWrite != nil {
			callback := fake.afterWrite
			fake.afterWrite = nil
			callback(fake)
		}
	}
	if fail {
		return nil, errors.New("simulated CLI failure")
	}
	return []byte("configured"), nil
}

func ownedTailscaleState(target string) *tailscaleFunnelState {
	state := newTailscaleFunnelState(testTailscaleNode(), target)
	state.Pending = false
	now := time.Now().UTC()
	state.VerifiedAt = &now
	return state
}

func TestTailscaleMappingLifecyclePreservesOtherPaths(t *testing.T) {
	target := "http://127.0.0.1:8765"
	original := testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/other": {Proxy: "http://127.0.0.1:9000"}})
	original.TCP["8443"] = &tailscaleTCPHandler{HTTPS: true}
	original.Web["device.example-tailnet.ts.net:8443"] = &tailscaleWebConfig{Handlers: map[string]*tailscaleHTTPHandler{"/": {Text: "foreign"}}}
	original.Services = map[string]json.RawMessage{"svc:other": json.RawMessage(`{"TCP":{"80":{"HTTP":true}}}`)}
	fake := newMemoryTailscale(original)
	change, err := prepareTailscaleMapping(fake.node, fake.config, target, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.apply(context.Background(), fake.client()); err != nil {
		t.Fatal(err)
	}
	if !fake.config.handler(fake.node.DNSName+":443", "/").isProxy(target) {
		t.Fatal("root not configured")
	}
	state := ownedTailscaleState(target)
	again, err := prepareTailscaleMapping(fake.node, fake.config, target, state, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.apply(context.Background(), fake.client()); err != nil {
		t.Fatal(err)
	}
	if fake.writeCount != 1 {
		t.Fatalf("non-idempotent start: %d writes", fake.writeCount)
	}
	stop, err := prepareTailscaleStop(fake.node, fake.config, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop.apply(context.Background(), fake.client()); err != nil {
		t.Fatal(err)
	}
	if !sameTailscaleServe(original, fake.config) {
		t.Fatalf("stop changed foreign config: %+v", fake.config)
	}
	for _, command := range fake.commands {
		joined := " " + strings.Join(command, " ") + " "
		for _, forbidden := range []string{" reset ", " down ", " logout ", " set-raw "} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("unsafe command: %s", joined)
			}
		}
	}
}

func TestTailscaleMappingAdoptionAndConflicts(t *testing.T) {
	target := "http://127.0.0.1:8765"
	for _, tc := range []struct {
		name     string
		public   bool
		handlers map[string]*tailscaleHTTPHandler
		state    *tailscaleFunnelState
		adopt    bool
		ok       bool
	}{
		{"same root explicit", true, map[string]*tailscaleHTTPHandler{"/": {Proxy: target}}, nil, true, true},
		{"same root without ownership", true, map[string]*tailscaleHTTPHandler{"/": {Proxy: target}}, nil, false, false},
		{"foreign root", true, map[string]*tailscaleHTTPHandler{"/": {Proxy: "http://127.0.0.1:9000"}}, nil, true, false},
		{"private sibling", false, map[string]*tailscaleHTTPHandler{"/private": {Proxy: "http://127.0.0.1:9000"}}, nil, true, false},
		{"foreign MCP", true, map[string]*tailscaleHTTPHandler{"/mcp": {Proxy: "http://127.0.0.1:9000/mcp"}}, nil, true, false},
		{"reserved OAuth", true, map[string]*tailscaleHTTPHandler{"/oauth": {Proxy: "http://127.0.0.1:9000"}}, nil, true, false},
		{"handler with policy", true, map[string]*tailscaleHTTPHandler{"/": {Proxy: target, AcceptAppCaps: []string{"test"}}}, nil, true, false},
		{"private owned root", false, map[string]*tailscaleHTTPHandler{"/": {Proxy: target}}, ownedTailscaleState(target), false, true},
		{"owned port migration", true, map[string]*tailscaleHTTPHandler{"/": {Proxy: "http://127.0.0.1:8764"}}, ownedTailscaleState("http://127.0.0.1:8764"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMemoryTailscale(testServeWithHandlers(tc.public, tc.handlers))
			_, err := prepareTailscaleMapping(fake.node, fake.config, target, tc.state, tc.adopt)
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v, error=%v", tc.ok, err)
			}
			if fake.writeCount != 0 {
				t.Fatal("preflight mutated configuration")
			}
		})
	}
}

func TestTailscaleTemporaryMCPMigrationAndRollback(t *testing.T) {
	target := "http://127.0.0.1:8765"
	original := testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/mcp": {Proxy: target + "/mcp"}})
	fake := newMemoryTailscale(original)
	change, err := prepareTailscaleMapping(fake.node, fake.config, target, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.apply(context.Background(), fake.client()); err != nil {
		t.Fatal(err)
	}
	if fake.config.handler(fake.node.DNSName+":443", "/mcp") != nil || !fake.config.handler(fake.node.DNSName+":443", "/").isProxy(target) {
		t.Fatal("temporary path not migrated")
	}
	if err := change.rollback(context.Background(), fake.client()); err != nil {
		t.Fatal(err)
	}
	if !sameTailscaleServe(original, fake.config) {
		t.Fatal("temporary MCP mapping was not restored")
	}
}

func TestTailscaleMappingFailuresDoNotReplayWrites(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(strconvBool(partial), func(t *testing.T) {
			original := testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/other": {Proxy: "http://127.0.0.1:9000"}})
			fake := newMemoryTailscale(original)
			fake.failWrite, fake.partialWrite = 1, partial
			change, err := prepareTailscaleMapping(fake.node, fake.config, "http://127.0.0.1:8765", nil, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := change.apply(context.Background(), fake.client()); err == nil {
				t.Fatal("expected failure")
			}
			if fake.writeCount != 1 {
				t.Fatalf("write was replayed: %d", fake.writeCount)
			}
			if err := change.rollback(context.Background(), fake.client()); err != nil {
				t.Fatal(err)
			}
			if !sameTailscaleServe(original, fake.config) {
				t.Fatal("rollback failed")
			}
		})
	}
}

func strconvBool(value bool) string {
	if value {
		return "partial_success"
	}
	return "no_write"
}

func TestTailscaleRollbackPreservesConcurrentForeignChanges(t *testing.T) {
	fake := newMemoryTailscale(&tailscaleServeConfig{})
	fake.afterWrite = func(f *memoryTailscaleCLI) {
		f.config.Web[f.node.DNSName+":443"].Handlers["/foreign"] = &tailscaleHTTPHandler{Proxy: "http://127.0.0.1:9000"}
	}
	change, err := prepareTailscaleMapping(fake.node, fake.config, "http://127.0.0.1:8765", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.apply(context.Background(), fake.client()); err == nil {
		t.Fatal("foreign write not detected")
	}
	if err := change.rollback(context.Background(), fake.client()); err != nil {
		t.Fatal(err)
	}
	if fake.config.handler(fake.node.DNSName+":443", "/") != nil || !fake.config.handler(fake.node.DNSName+":443", "/foreign").isProxy("http://127.0.0.1:9000") {
		t.Fatal("rollback damaged foreign configuration")
	}
}

func TestTailscaleStopRejectsModifiedOwnershipAndDevice(t *testing.T) {
	fake := newMemoryTailscale(testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/": {Proxy: "http://127.0.0.1:9000"}}))
	state := ownedTailscaleState("http://127.0.0.1:8765")
	if _, err := prepareTailscaleStop(fake.node, fake.config, state); tailscaleDiagnosticCode(err) != "ownership_conflict" {
		t.Fatal(err)
	}
	state.DeviceID = "different-node"
	if _, err := prepareTailscaleStop(fake.node, fake.config, state); tailscaleDiagnosticCode(err) != "device_changed" {
		t.Fatal(err)
	}
	if fake.writeCount != 0 {
		t.Fatal("stop changed a foreign mapping")
	}
}

func TestTailscaleMappingChecksAgainBeforeWrite(t *testing.T) {
	fake := newMemoryTailscale(&tailscaleServeConfig{})
	change, err := prepareTailscaleMapping(fake.node, fake.config, "http://127.0.0.1:8765", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	fake.config = testServeWithHandlers(true, map[string]*tailscaleHTTPHandler{"/": {Proxy: "http://127.0.0.1:9000"}})
	if err := change.apply(context.Background(), fake.client()); tailscaleDiagnosticCode(err) != "configuration_changed" {
		t.Fatal(err)
	}
	if fake.writeCount != 0 {
		t.Fatal("stale plan wrote configuration")
	}
}

func TestTailscaleOwnershipValidation(t *testing.T) {
	for _, target := range []string{"http://localhost:8765", "http://127.0.0.1:0", "http://127.0.0.1:65536", "https://127.0.0.1:8765", "http://127.0.0.1:8765/mcp", "http://user@127.0.0.1:8765", "http://127.0.0.1:8765?"} {
		if validTailscaleLocalOrigin(target) {
			t.Errorf("accepted %s", target)
		}
	}
	state := ownedTailscaleState("http://127.0.0.1:8765")
	if err := state.validate(); err != nil {
		t.Fatal(err)
	}
	state.LegacyMCPProxy = "http://127.0.0.1:9000/mcp"
	if err := state.validate(); err == nil {
		t.Fatal("accepted inconsistent legacy ownership")
	}
}
