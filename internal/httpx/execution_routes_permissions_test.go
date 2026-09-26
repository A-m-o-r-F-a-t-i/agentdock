package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionPermissionUpdateReturnsEffectiveReadbackAndRejectsUnauthorized(t *testing.T) {
	f := newExecutionHTTPFixture(t)
	settings := map[string]any{
		"permission_profile": map[string]any{"filesystem": "deny", "network": "deny", "sandbox_boundary": "workspace"},
		"approval_policy":    map[string]any{"mode": "never"},
		"approval_reviewer":  "auto_review",
	}
	change := map[string]any{
		"scope":                      "global",
		"mode":                       "full",
		"confirm_full":               true,
		"expected_revision":          1,
		"custom_permissions_enabled": false,
		"settings":                   settings,
	}
	raw, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, f.server.URL+"/internal/runtime/permissions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized permission save status=%d", response.StatusCode)
	}
	policyPath := filepath.Join(f.root, ".agentdock", "execution", "permissions", "policy.json")
	if _, err = os.Stat(policyPath); !os.IsNotExist(err) {
		t.Fatalf("unauthorized save created policy: %v", err)
	}

	result, status := f.request(t, http.MethodPost, "/internal/runtime/permissions", change)
	if status != http.StatusOK || result["effective_readback_available"] != true {
		t.Fatalf("permission save/readback failed: status=%d result=%+v", status, result)
	}
	policy := result["policy"].(map[string]any)
	effective := result["effective"].(map[string]any)
	if policy["schema_version"] != float64(3) || policy["revision"] != float64(2) || policy["custom_permissions_enabled"] != false {
		t.Fatalf("saved policy mismatch: %+v", policy)
	}
	if effective["custom_permissions_enabled"] != false || effective["settings_source"] != "execution_mode" || effective["custom_permissions_scope"] != "global" {
		t.Fatalf("disabled effective metadata mismatch: %+v", effective)
	}
	actual := effective["settings"].(map[string]any)
	configured := effective["configured_settings"].(map[string]any)
	if actual["permission_profile"].(map[string]any)["network"] != "allow" || actual["approval_policy"].(map[string]any)["mode"] != "on-request" || actual["approval_reviewer"] != "user" {
		t.Fatalf("disabled custom settings still effective: %+v", actual)
	}
	if configured["permission_profile"].(map[string]any)["network"] != "deny" || configured["approval_policy"].(map[string]any)["mode"] != "never" || configured["approval_reviewer"] != "auto_review" {
		t.Fatalf("disabled custom history was lost: %+v", configured)
	}

	enabled, status := f.request(t, http.MethodPost, "/internal/runtime/permissions", map[string]any{
		"scope": "global", "expected_revision": 2, "custom_permissions_enabled": true,
	})
	if status != http.StatusOK || enabled["effective_readback_available"] != true {
		t.Fatalf("enable/readback failed: status=%d result=%+v", status, enabled)
	}
	effective = enabled["effective"].(map[string]any)
	if effective["custom_permissions_enabled"] != true || effective["settings_source"] != "custom_permissions" || effective["settings"].(map[string]any)["approval_reviewer"] != "auto_review" {
		t.Fatalf("enabled history not restored: %+v", effective)
	}
	stale, status := f.request(t, http.MethodPost, "/internal/runtime/permissions", map[string]any{
		"scope": "global", "expected_revision": 2, "custom_permissions_enabled": false,
	})
	if status != http.StatusConflict {
		t.Fatalf("stale permission save accepted: status=%d result=%+v", status, stale)
	}
}
