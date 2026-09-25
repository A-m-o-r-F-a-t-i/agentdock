package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireRG(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		if os.Getenv("AGENTDOCK_TEST_REQUIRE_RG") == "1" {
			t.Fatalf("real ripgrep is required for this verification: %v", err)
		}
		t.Skip("real ripgrep is not available on PATH")
	}
	// User configuration must not change a fixture's search mode or output.
	t.Setenv("RIPGREP_CONFIG_PATH", "")
}

func TestSearchTextRGQueryArguments(t *testing.T) {
	requireRG(t)
	for _, query := range []string{"--help", "-needle", "--", "--files", "--한글", "ordinary needle"} {
		for _, regex := range []bool{false, true} {
			for _, singleFile := range []bool{false, true} {
				name := query + "/literal/directory"
				if regex {
					name = query + "/regex/directory"
				}
				if singleFile {
					name += "/file"
				}
				t.Run(name, func(t *testing.T) {
					rt, root := newCodeToolsRuntime(t)
					dir := filepath.Join(root, "한글 경로 with spaces")
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
					file := filepath.Join(dir, "-sample file.txt")
					if err := os.WriteFile(file, []byte("before\n"+query+"\nafter\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					path := dir
					if singleFile {
						path = file
					}
					result, err := rt.searchTextTest(context.Background(), SearchRequest{
						Path: path, Query: query, Regex: regex, CaseSensitive: true,
						IncludeIgnored: true, IncludeHidden: true, MaxResults: intPtrForTest(10),
					})
					if err != nil {
						t.Fatal(err)
					}
					if result["engine"] != "rg" {
						t.Fatalf("real rg was not exercised: %#v", result)
					}
					matches, ok := result["matches"].([]map[string]any)
					if !ok || len(matches) != 1 || result["total_matches"] != 1 {
						t.Fatalf("query %q must be a pattern, not an option: %#v", query, result)
					}
					wantPath, err := rt.ws.Relative(file)
					if err != nil {
						t.Fatal(err)
					}
					if matches[0]["path"] != wantPath || matches[0]["line"] != 2 || matches[0]["column"] != 1 || matches[0]["match_text"] != query {
						t.Fatalf("wrong match identity/position: %#v", matches[0])
					}
				})
			}
		}
	}
}

func TestSearchTextRGExitCodes(t *testing.T) {
	requireRG(t)
	rt, root := newCodeToolsRuntime(t)
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("-needle42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := rt.searchTextTest(context.Background(), SearchRequest{Path: root, Query: "-needle[0-9]+", Regex: true, IncludeIgnored: true})
	if err != nil || result["engine"] != "rg" || result["total_matches"] != 1 {
		t.Fatalf("exit 0 / real regex semantics not preserved: result=%#v error=%v", result, err)
	}
	result, err = rt.searchTextTest(context.Background(), SearchRequest{Path: root, Query: "not-present", IncludeIgnored: true})
	if err != nil || result["engine"] != "rg" || result["total_matches"] != 0 {
		t.Fatalf("exit 1 must remain a successful empty result: result=%#v error=%v", result, err)
	}
	_, err = rt.searchTextTest(context.Background(), SearchRequest{Path: root, Query: "[", Regex: true, IncludeIgnored: true})
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "SEARCH_FAILED" || toolErr.Details["engine"] != "rg" {
		t.Fatalf("invalid rg regex must not silently fall back: %#v", err)
	}
}

func TestSearchTextMissingRGUsesGoFallback(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if path, err := exec.LookPath("rg"); err == nil {
		t.Fatalf("fixture did not remove rg from PATH: %s", path)
	}
	rt, root := newCodeToolsRuntime(t)
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("--help\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := rt.searchTextTest(context.Background(), SearchRequest{Path: root, Query: "--help", IncludeIgnored: true})
	if err != nil || result["engine"] != "go_fallback" || result["total_matches"] != 1 {
		t.Fatalf("rg absence must preserve Go fallback: result=%#v error=%v", result, err)
	}
}
