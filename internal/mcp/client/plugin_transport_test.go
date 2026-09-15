package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPackageHeadersDoNotCrossRedirectOrigin(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Private-Reference") != "literal-value" {
			t.Error("configured header missing")
		}
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := newOriginBoundHTTPClient(source.URL, http.Header{"X-Private-Reference": []string{"literal-value"}})
	response, err := client.Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || received.Load() != 0 {
		t.Fatal("configured header could travel to another origin")
	}
}

func TestPackageEnvironmentAndClientHeadersHaveSpecifiedPrecedence(t *testing.T) {
	environment, err := stdioEnvironment(ServerConfig{Name: "test", RuntimeEnv: map[string]string{"VALUE": "base", "PLUGIN_ROOT": "wrong"}, PackageEnv: map[string]string{"VALUE": "  literal ${UNKNOWN}  "}, PluginRoot: "actual-root", PluginData: "actual-data"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "VALUE=  literal ${UNKNOWN}  ") || !strings.Contains(joined, "PLUGIN_ROOT=actual-root") || !strings.Contains(joined, "PLUGIN_DATA=actual-data") {
		t.Fatal("package env or reserved host env has wrong precedence")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "client-value" || r.Header.Get("X-Literal") != "${UNKNOWN}" {
			t.Error("client header lost precedence or literal was expanded")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := newOriginBoundHTTPClient(server.URL, http.Header{"Authorization": []string{"package-value"}, "X-Literal": []string{"${UNKNOWN}"}})
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Authorization", "client-value")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}
