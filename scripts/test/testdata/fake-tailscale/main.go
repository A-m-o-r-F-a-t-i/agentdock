// Fake Tailscale is a test-only executable. It never contacts tailscaled.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type fixture struct {
	BackendState string         `json:"backend_state,omitempty"`
	Offline      bool           `json:"offline,omitempty"`
	NoCapability bool           `json:"no_capability,omitempty"`
	Config       map[string]any `json:"config"`
	Commands     [][]string     `json:"commands,omitempty"`
	Writes       int            `json:"writes,omitempty"`
	FailWrite    int            `json:"fail_write,omitempty"`
	PartialWrite bool           `json:"partial_write,omitempty"`
	DelayMS      int            `json:"delay_ms,omitempty"`
	OutputBytes  int            `json:"output_bytes,omitempty"`
}

const dnsName = "device.example-tailnet.ts.net"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(23)
	}
}

func run() error {
	path := os.Getenv("FAKE_TAILSCALE_STATE")
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("FAKE_TAILSCALE_STATE must name an absolute fixture file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("fixture too large")
	}
	var state fixture
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.DelayMS > 0 {
		time.Sleep(time.Duration(min(state.DelayMS, 30000)) * time.Millisecond)
	}
	if state.OutputBytes > 0 {
		fmt.Fprint(os.Stdout, strings.Repeat("x", min(state.OutputBytes, 2<<20)))
		return nil
	}
	args := os.Args[1:]
	if len(state.Commands) >= 2000 {
		return errors.New("fixture command limit exceeded")
	}
	state.Commands = append(state.Commands, append([]string(nil), args...))
	persist := func() error {
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return os.WriteFile(path, encoded, 0o600)
	}
	if err := persist(); err != nil {
		return err
	}
	if len(args) == 3 && args[0] == "status" && args[1] == "--json" && args[2] == "--peers=false" {
		backend := state.BackendState
		if backend == "" {
			backend = "Running"
		}
		caps := map[string]any{}
		if !state.NoCapability {
			caps["funnel"] = nil
			caps["https"] = nil
			caps["https://tailscale.com/cap/funnel-ports?ports=443,8443,10000"] = nil
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"BackendState": backend, "Self": map[string]any{"ID": "fake-node", "HostName": "device", "DNSName": dnsName + ".", "Online": !state.Offline, "CapMap": caps}})
	}
	if len(args) == 3 && args[0] == "funnel" && args[1] == "status" && args[2] == "--json" {
		return json.NewEncoder(os.Stdout).Encode(state.Config)
	}
	if len(args) != 6 || (args[0] != "funnel" && args[0] != "serve") || args[1] != "--bg" || args[2] != "--yes" || args[3] != "--https=443" || (args[4] != "--set-path=/" && args[4] != "--set-path=/mcp") {
		return errors.New("unexpected CLI operation: " + strings.Join(args, " "))
	}
	state.Writes++
	fail := state.FailWrite == state.Writes
	if !fail || state.PartialWrite {
		if state.Config == nil {
			state.Config = map[string]any{}
		}
		tcp := object(state.Config, "TCP")
		web := object(state.Config, "Web")
		allow := object(state.Config, "AllowFunnel")
		hostPort := dnsName + ":443"
		handlers := object(object(web, hostPort), "Handlers")
		mount := strings.TrimPrefix(args[4], "--set-path=")
		if args[5] == "off" {
			delete(handlers, mount)
			if len(handlers) == 0 {
				delete(web, hostPort)
				delete(tcp, "443")
				delete(allow, hostPort)
			}
		} else {
			if !strings.HasPrefix(args[5], "http://127.0.0.1:") {
				return errors.New("non-loopback fixture target")
			}
			port := strings.TrimSuffix(strings.TrimPrefix(args[5], "http://127.0.0.1:"), "/mcp")
			if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
				return errors.New("invalid fixture port")
			}
			tcp["443"] = map[string]any{"HTTPS": true}
			handlers[mount] = map[string]any{"Proxy": args[5]}
			if args[0] == "funnel" {
				allow[hostPort] = true
			} else {
				delete(allow, hostPort)
			}
		}
	}
	if err := persist(); err != nil {
		return err
	}
	if fail {
		return errors.New("simulated failure with private-test-secret-not-for-logs")
	}
	fmt.Fprintln(os.Stdout, "configured")
	return nil
}

func object(parent map[string]any, key string) map[string]any {
	value, ok := parent[key].(map[string]any)
	if !ok {
		value = map[string]any{}
		parent[key] = value
	}
	return value
}
