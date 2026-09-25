package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	oauthClientPending    = "pending"
	oauthClientAuthorized = "authorized"
	// Registration-to-consent time is independent of authorization-code TTL
	// and the normal idle lifetime. Ordinary unauthenticated lookups do not
	// extend this fixed deadline.
	oauthPendingClientTTL  = time.Hour
	maxPendingOAuthClients = 256
	maxPendingOAuthBytes   = maxOAuthStateSize / 4
)

var ErrOAuthClientMetadata = errors.New("invalid OAuth client metadata")

type OAuthCapacityError struct {
	Pool       string
	RetryAfter time.Duration
}

func (e *OAuthCapacityError) Error() string {
	return fmt.Sprintf("OAuth %s capacity is temporarily unavailable", e.Pool)
}

func (s *OAuthStore) pendingClientUsageLocked(now int64) (count, bytes int, retry time.Duration) {
	until := now + int64(oauthPendingClientTTL/time.Second)
	for id, registration := range s.clients {
		if registration.Lifecycle != oauthClientPending {
			continue
		}
		count++
		encoded, _ := json.Marshal(registration)
		bytes += len(encoded) + len(id) + 4
		until = min(until, registration.PendingUntil)
	}
	return count, bytes, time.Duration(max(1, until-now)) * time.Second
}

// Prepare without changing state; the caller publishes the registration in
// the same durable transaction as the authorization outcome.
func (s *OAuthStore) prepareAuthorizedClientLocked(clientID string, now int64) (OAuthClientRegistration, error) {
	registration, ok := s.clients[clientID]
	if !ok || clientRegistrationExpired(registration, now) {
		return OAuthClientRegistration{}, errors.New("OAuth registration expired before authorization")
	}
	if registration.Lifecycle == oauthClientAuthorized || registration.Lifecycle == "" {
		return registration, nil
	}
	count := 0
	for _, client := range s.clients {
		if client.Lifecycle != oauthClientPending && !clientRegistrationExpired(client, now) {
			count++
		}
	}
	if count >= maxOAuthClients {
		return OAuthClientRegistration{}, &OAuthCapacityError{Pool: "authorized clients", RetryAfter: time.Minute}
	}
	registration.Lifecycle, registration.PendingUntil, registration.LastUsedAt = oauthClientAuthorized, 0, now
	return registration, nil
}

func (s *OAuthStore) authorizeClientLocked(clientID string, now int64) error {
	previous := s.clients[clientID]
	registration, err := s.prepareAuthorizedClientLocked(clientID, now)
	if err != nil {
		return err
	}
	if registration.Lifecycle == previous.Lifecycle {
		return nil
	}
	s.clients[clientID] = registration
	if err = s.persistStateLocked(); err != nil {
		s.clients[clientID] = previous
		return err
	}
	return nil
}
