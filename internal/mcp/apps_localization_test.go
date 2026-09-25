package mcp

import (
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/uvwt/agentdock-protocol/mcpapps"
)

var localizedAppViews = []string{"agentdock_context", "task_progress", "file_change", "dynamic_mcp", "artifact", "recall", "workflow", "acp_status"}

func TestMCPAppKoreanDictionaryAndRendererContract(t *testing.T) {
	dictionary := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(string(koreanAppMessages)))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatal("invalid Korean dictionary")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		key := token.(string)
		if _, exists := dictionary[key]; exists {
			t.Fatalf("duplicate Korean key %s", key)
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(value) == "" {
			t.Fatalf("empty Korean key %s", key)
		}
		dictionary[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatal("trailing dictionary data")
	}
	neutral := mcpapps.HTML("task_progress", "Task")
	start := strings.Index(neutral, "    en:{")
	if start < 0 {
		t.Fatal("pinned dictionary anchor changed")
	}
	end := strings.Index(neutral[start:], "\n    },")
	if end < 0 {
		t.Fatal("pinned dictionary boundary changed")
	}
	fields := regexp.MustCompile(`(\w+):("(?:[^"\\]|\\.)*")`).FindAllStringSubmatch(neutral[start:start+end], -1)
	if len(fields) < 100 {
		t.Fatal("incomplete upstream message inventory")
	}
	placeholders := regexp.MustCompile(`\{\w+\}`)
	for _, field := range fields {
		var english string
		if err := json.Unmarshal([]byte(field[2]), &english); err != nil {
			t.Fatal(err)
		}
		korean, exists := dictionary[field[1]]
		if !exists {
			t.Errorf("Korean translation missing: %s", field[1])
			continue
		}
		before := placeholders.FindAllString(english, -1)
		after := placeholders.FindAllString(korean, -1)
		sort.Strings(before)
		sort.Strings(after)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("placeholder mismatch: %s", field[1])
		}
	}
	for _, view := range localizedAppViews {
		t.Run(view, func(t *testing.T) {
			page := localizedAppHTML(view, "Fixture title")
			if dictionary["title_"+view] == "" {
				t.Fatal("missing localized document title")
			}
			for _, required := range []string{`messages["ko-KR"]`, `candidate==="ko"`, `event.source!==window.parent`, `message.jsonrpc!=="2.0"`, `connect-src 'none'`, `message.params&&message.params.structuredContent`, `refreshLocalePresentation()`, `stateLabel(session.status||state.status)`, `t("role_"+message.role)`} {
				if !strings.Contains(page, required) {
					t.Errorf("missing renderer/security contract %q", required)
				}
			}
			for _, forbidden := range []string{"innerHTML", `rpcRequest("tools/call"`, `rpcRequest("mcp_tool_call"`, `item.append(el("div","message-role",message.role))`} {
				if strings.Contains(page, forbidden) {
					t.Errorf("unsafe or untranslated renderer fragment %q", forbidden)
				}
			}
			rpcPattern := regexp.MustCompile(`rpc(?:Request|Notify)\("[^"]+"`)
			if !reflect.DeepEqual(rpcPattern.FindAllString(page, -1), rpcPattern.FindAllString(mcpapps.HTML(view, "Fixture title"), -1)) {
				t.Fatal("localization changed the MCP App wire calls")
			}
		})
	}
}
