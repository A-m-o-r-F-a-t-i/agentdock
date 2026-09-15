package desktopruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeOptionsDefaultsAndRoundTrip(t *testing.T) {
	var options RuntimeOptions
	data, _ := json.Marshal(map[string]any{"default_dir": t.TempDir(), "agents_autoload": false})
	if err := json.Unmarshal(data, &options); err != nil {
		t.Fatal(err)
	}
	if options.AgentsAutoLoad || options.ACPMaxPrompts != 2 || options.ACPInteractionMS != 300000 {
		t.Fatalf("wrong defaults: %#v", options)
	}
	options, err := normalizeRuntimeOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RuntimeOptions
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	env, err := decoded.environment()
	if err != nil {
		t.Fatal(err)
	}
	if env["AGENTDOCK_DEFAULT_DIR"] != options.DefaultDir || env["AGENTDOCK_AGENTS_AUTOLOAD"] != "false" || env["AGENTDOCK_ACP_INTERACTION_TIMEOUT_MS"] != "300000" {
		t.Fatalf("wrong environment: %#v", env)
	}
}

func TestRuntimeOptionsRejectInvalidBeforeWrite(t *testing.T) {
	root := t.TempDir()
	for name, modify := range map[string]func(*RuntimeOptions){
		"relative directory":                  func(o *RuntimeOptions) { o.DefaultDir = "relative" },
		"missing explicit instructions":       func(o *RuntimeOptions) { o.InstructionsFile = filepath.Join(root, "missing.md") },
		"missing browser":                     func(o *RuntimeOptions) { o.BrowserExecutablePath = filepath.Join(root, "missing.exe") },
		"concurrency zero":                    func(o *RuntimeOptions) { o.ACPMaxPrompts = 0 },
		"concurrency high":                    func(o *RuntimeOptions) { o.ACPMaxPrompts = 9 },
		"timeout low":                         func(o *RuntimeOptions) { o.ACPInteractionMS = 999 },
		"timeout high":                        func(o *RuntimeOptions) { o.ACPInteractionMS = 3600001 },
		"invalid CIDR":                        func(o *RuntimeOptions) { o.TrustedProxyCIDRs = []string{"all"} },
		"literal secret instead of reference": func(o *RuntimeOptions) { o.CommandEnvFromEnv = map[string]string{"CHILD": "not a variable name"} },
	} {
		t.Run(name, func(t *testing.T) {
			o := defaultRuntimeOptions()
			o.DefaultDir = filepath.Join(root, "not-created")
			modify(&o)
			if _, err := normalizeRuntimeOptions(o); err == nil {
				t.Fatal("accepted invalid options")
			}
			if _, err := os.Stat(filepath.Join(root, "not-created")); !os.IsNotExist(err) {
				t.Fatal("validation wrote the workspace")
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{"unknown":true}`, `{"agents_autoload":"false"}`, `{"acp_interaction_timeout_ms":"1000"}`} {
		var options RuntimeOptions
		if json.Unmarshal([]byte(raw), &options) == nil {
			t.Fatalf("accepted invalid options JSON %s", raw)
		}
	}
}

func TestRuntimeOptionsNormalizeCIDRsAndExplicitInstructions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rules.md")
	if err := os.WriteFile(path, []byte("保持当前工程规则。"), 0600); err != nil {
		t.Fatal(err)
	}
	options := defaultRuntimeOptions()
	options.DefaultDir = root
	options.InstructionsFile = path
	options.TrustedProxyCIDRs = []string{" 127.0.0.1/8 ", "127.0.0.0/8"}
	options, err := normalizeRuntimeOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(options.TrustedProxyCIDRs, ",") != "127.0.0.0/8" {
		t.Fatalf("unexpected CIDRs: %#v", options.TrustedProxyCIDRs)
	}
}
