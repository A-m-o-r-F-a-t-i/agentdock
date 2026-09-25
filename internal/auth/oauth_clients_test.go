package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gooauth2 "github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/models"
)

func registerPending(t *testing.T, s *OAuthStore) string {
	t.Helper()
	id, err := s.RegisterClient("quota-test", []string{"https://client.example/callback"}, []string{"authorization_code", "refresh_token"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func authorizationFixture(client string) *models.Token {
	info := models.NewToken()
	info.SetClientID(client)
	info.SetUserID("fixture-owner")
	info.SetRedirectURI("https://client.example/callback")
	info.SetScope("https://server.example/mcp")
	info.SetExtension(url.Values{oauthResourceExtension: {"https://server.example/mcp"}})
	info.SetCode("fixture-code")
	info.SetCodeChallenge(strings.Repeat("A", 43))
	info.SetCodeCreateAt(time.Now())
	info.SetCodeExpiresIn(5 * time.Minute)
	return info
}

func TestOAuthPendingFloodDoesNotConsumeAuthorizedCapacity(t *testing.T) {
	s := NewOAuthStore()
	authorized := registerPending(t, s)
	if err := s.storeAuthorizationCode("authorized-code", authorizationFixture(authorized)); err != nil {
		t.Fatal(err)
	}
	for range maxPendingOAuthClients {
		registerPending(t, s)
	}
	_, err := s.RegisterClient("overflow", []string{"https://client.example/callback"}, []string{"authorization_code"})
	var capacity *OAuthCapacityError
	if !errors.As(err, &capacity) || capacity.RetryAfter <= 0 || capacity.RetryAfter > oauthPendingClientTTL {
		t.Fatalf("pending capacity: %v", err)
	}
	if client, ok := s.ClientRegistration(authorized); !ok || client.Lifecycle != oauthClientAuthorized {
		t.Fatal("pending flood evicted authorization")
	}
	if _, ok := s.codes["authorized-code"]; !ok {
		t.Fatal("pending flood removed authorized code")
	}
	now := time.Now().Unix()
	for id, client := range s.clients {
		if client.Lifecycle == oauthClientPending {
			client.PendingUntil = now - 1
			s.clients[id] = client
		}
	}
	newID := registerPending(t, s)
	if len(s.clients) != 2 || !s.ValidateClientID(newID) || !s.ValidateClientID(authorized) {
		t.Fatal("pending reclamation affected authorized clients")
	}
}

func TestOAuthPendingDeadlineFixedAndRestarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	s, err := NewPersistentOAuthStore(path, "signing-key")
	if err != nil {
		t.Fatal(err)
	}
	id := registerPending(t, s)
	initial := s.clients[id]
	for range 5 {
		if !s.ValidateClientID(id) || !s.ClientAllowsGrant(id, "authorization_code") {
			t.Fatal("pending registration missing")
		}
	}
	if s.clients[id].PendingUntil != initial.PendingUntil {
		t.Fatal("unauthenticated lookup extended pending registration")
	}
	reloaded, err := NewPersistentOAuthStore(path, "signing-key")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.clients[id].PendingUntil != initial.PendingUntil || reloaded.clients[id].Lifecycle != oauthClientPending {
		t.Fatal("restart changed pending deadline")
	}
	client := reloaded.clients[id]
	client.PendingUntil = time.Now().Add(-time.Second).Unix()
	reloaded.clients[id] = client
	reloaded.codes["pending-code"] = OAuthCode{ClientID: id, ExpiresAt: time.Now().Add(time.Hour)}
	reloaded.grants["associated"] = OAuthGrant{ClientID: id, AccessTokenHash: "hash"}
	reloaded.accessIndex["hash"] = "associated"
	if reloaded.ValidateClientID(id) || len(reloaded.codes) != 0 || len(reloaded.grants) != 0 || len(reloaded.accessIndex) != 0 {
		t.Fatal("expiration did not remove all pending indices")
	}
	reloaded, err = NewPersistentOAuthStore(path, "signing-key")
	if err != nil || len(reloaded.clients) != 0 {
		t.Fatalf("expiry did not persist: %v", err)
	}
}

func TestOAuthPendingPromotionRequiresSuccessfulStorage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oauth.json")
	s, err := NewPersistentOAuthStore(path, "signing-key")
	if err != nil {
		t.Fatal(err)
	}
	id := registerPending(t, s)
	before := s.clients[id]
	blocked := filepath.Join(root, "blocked")
	if err = os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.statePath = filepath.Join(blocked, "oauth.json")
	if err = s.storeAuthorizationCode("not-issued", authorizationFixture(id)); err == nil {
		t.Fatal("failed promotion was reported successful")
	}
	if s.clients[id].Lifecycle != before.Lifecycle || s.clients[id].PendingUntil != before.PendingUntil || len(s.codes) != 0 {
		t.Fatal("failed promotion altered pending state or issued code")
	}
	client := s.clients[id]
	client.PendingUntil = time.Now().Add(-time.Second).Unix()
	s.clients[id] = client
	if s.ValidateClientID(id) {
		t.Fatal("failed cleanup revived an expired registration")
	}
	if len(s.clients) != 1 {
		t.Fatal("failed cleanup silently removed durable state")
	}
	s.statePath = path
	s.clients[id] = before
	if err = s.storeAuthorizationCode("issued", authorizationFixture(id)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewPersistentOAuthStore(path, "signing-key")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.clients[id].Lifecycle != oauthClientAuthorized || reloaded.clients[id].PendingUntil != 0 {
		t.Fatal("successful promotion not durable")
	}
}

func TestOAuthPendingAuthorizationAfterSimulatedConsentDelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	s, err := NewPersistentOAuthStore(path, "key")
	if err != nil {
		t.Fatal(err)
	}
	id := registerPending(t, s)
	client := s.clients[id]
	client.IssuedAt = time.Now().Add(-50 * time.Minute).Unix()
	client.LastUsedAt = client.IssuedAt
	client.PendingUntil = client.IssuedAt + int64(oauthPendingClientTTL/time.Second)
	s.clients[id] = client
	manager := NewOAuthManager(s, "key", 3600, 90*24*time.Hour)
	verifier := strings.Repeat("v", 43)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	request := &gooauth2.TokenGenerateRequest{ClientID: id, UserID: "owner", RedirectURI: client.RedirectURIs[0], CodeChallenge: challenge, CodeChallengeMethod: gooauth2.CodeChallengeS256, Request: httptest.NewRequest(http.MethodGet, "/oauth/authorize?resource="+url.QueryEscape("https://server.example/mcp"), nil)}
	code, err := manager.GenerateAuthToken(context.Background(), gooauth2.Code, request)
	if err != nil {
		t.Fatal(err)
	}
	token, err := manager.GenerateAccessToken(WithOAuthRequest(context.Background(), "https://server.example", "https://server.example/mcp", id), gooauth2.AuthorizationCode, &gooauth2.TokenGenerateRequest{ClientID: id, Code: code.GetCode(), RedirectURI: client.RedirectURIs[0], CodeVerifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if token.GetAccess() == "" || token.GetRefresh() == "" || s.clients[id].Lifecycle != oauthClientAuthorized {
		t.Fatal("PKCE flow did not authorize delayed client")
	}
	reloaded, err := NewPersistentOAuthStore(path, "key")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.ValidateClientID(id) || reloaded.clients[id].PendingUntil != 0 {
		t.Fatal("restart lost delayed authorization")
	}
	for range maxPendingOAuthClients {
		registerPending(t, reloaded)
	}
	manager = NewOAuthManager(reloaded, "key", 3600, 90*24*time.Hour)
	refreshed, err := manager.RefreshAccessToken(WithOAuthRequest(context.Background(), "https://server.example", "https://server.example/mcp", id), &gooauth2.TokenGenerateRequest{ClientID: id, Refresh: token.GetRefresh()})
	if err != nil || refreshed.GetAccess() == "" {
		t.Fatalf("pending flood prevented existing grant refresh: %v", err)
	}
}

func TestOAuthPendingByteBudgetAndAuthorizedLimit(t *testing.T) {
	s := NewOAuthStore()
	longURI := "https://client.example/" + strings.Repeat("p", maxPendingOAuthBytes/2)
	if _, err := s.RegisterClient("large", []string{longURI}, []string{"authorization_code"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.RegisterClient("large2", []string{longURI + "x"}, []string{"authorization_code"})
	var capacity *OAuthCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("pending byte cap not enforced: %v", err)
	}
	_, err = s.RegisterClient("too-large", []string{longURI + longURI}, []string{"authorization_code"})
	if !errors.Is(err, ErrOAuthClientMetadata) {
		t.Fatalf("oversized single registration not classified: %v", err)
	}
	s = NewOAuthStore()
	id := registerPending(t, s)
	for i := 0; i < maxOAuthClients; i++ {
		s.clients[fmt.Sprintf("authorized-%d", i)] = OAuthClientRegistration{IssuedAt: time.Now().Unix(), LastUsedAt: time.Now().Unix(), Lifecycle: oauthClientAuthorized}
	}
	if err = s.storeAuthorizationCode("no-capacity", authorizationFixture(id)); !errors.As(err, &capacity) {
		t.Fatalf("authorized pool cap: %v", err)
	}
	if s.clients[id].Lifecycle != oauthClientPending || len(s.codes) != 0 || len(s.clients) != maxOAuthClients+1 {
		t.Fatal("capacity failure mutated client lifecycle")
	}
}

func TestOAuthPendingLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.json")
	s, err := NewPersistentOAuthStore(path, "key")
	if err != nil {
		t.Fatal(err)
	}
	pending, authorized := registerPending(t, s), registerPending(t, s)
	for _, id := range []string{pending, authorized} {
		client := s.clients[id]
		client.Lifecycle = ""
		client.PendingUntil = 0
		s.clients[id] = client
	}
	grantID, _ := RandomToken(24)
	s.grants[grantID] = OAuthGrant{ClientID: authorized, Resource: "https://server.example/mcp", ExpiresAt: time.Now().Add(time.Hour).Unix(), AccessTokenHash: "previous-grant"}
	if err = s.persistStateLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewPersistentOAuthStore(path, "key")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.clients[pending].Lifecycle != oauthClientPending || reloaded.clients[pending].PendingUntil <= time.Now().Unix() || reloaded.clients[authorized].Lifecycle != oauthClientAuthorized {
		t.Fatal("migration did not preserve existing consent")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state oauthState
	if err = json.Unmarshal(data, &state); err != nil || state.Version != 1 {
		t.Fatalf("state migration format changed unexpectedly: %v", err)
	}
	if _, err = url.Parse(state.Clients[pending].RedirectURIs[0]); err != nil {
		t.Fatal(err)
	}
}
