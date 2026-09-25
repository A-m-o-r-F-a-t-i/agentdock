package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/config"
)

func TestOAuthCapacityErrorMapping(t *testing.T) {
	for _, test := range []struct {
		err         error
		status      int
		code, retry string
	}{
		{auth.ErrOAuthClientMetadata, 400, "invalid_client_metadata", ""},
		{&auth.OAuthCapacityError{Pool: "pending", RetryAfter: 30 * time.Second}, 503, "temporarily_unavailable", "30"},
		{errors.New("private-storage-failure"), 500, "server_error", ""},
	} {
		recorder := httptest.NewRecorder()
		writeOAuthRegistrationError(recorder, test.err)
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != test.status || body["error"] != test.code || recorder.Header().Get("Retry-After") != test.retry || strings.Contains(recorder.Body.String(), "private-storage") {
			t.Fatalf("wrong error mapping: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	protocol := newOAuthProtocolServer(config.Config{}, auth.NewOAuthStore())
	body, status, headers := protocol.GetErrorData(&auth.OAuthCapacityError{Pool: "authorized", RetryAfter: time.Minute})
	if status != 503 || body["error"] != "temporarily_unavailable" || headers.Get("Retry-After") != "60" {
		t.Fatal("framework lost capacity status")
	}
}

func TestOAuthRegisterCapacityHTTP(t *testing.T) {
	store := auth.NewOAuthStore()
	cfg := config.Config{OAuthEnabled: true}
	metadata := `{"client_name":"quota","redirect_uris":["https://client.example/cb"],"token_endpoint_auth_method":"none","grant_types":["authorization_code"]}`
	accepted := 0
	for range 1025 {
		request := httptest.NewRequest(http.MethodPost, "https://server.example/register", strings.NewReader(metadata))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handleRegister(recorder, request, cfg, store)
		if recorder.Code == http.StatusCreated {
			accepted++
			continue
		}
		if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" || !strings.Contains(recorder.Body.String(), "temporarily_unavailable") {
			t.Fatalf("capacity mapped to %d: %s", recorder.Code, recorder.Body.String())
		}
		if accepted == 0 || accepted >= 1024 {
			t.Fatalf("pending registrations share old full-client quota: %d", accepted)
		}
		return
	}
	t.Fatal("registration capacity was not bounded")
}
