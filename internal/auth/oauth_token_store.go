package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	gooauth2 "github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/models"
)

func (s *OAuthStore) configureOAuthFramework(signingKey string, accessTTLSeconds int64, refreshTTL time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value := strings.TrimSpace(signingKey); value != "" {
		s.signingKey = value
	}
	if accessTTLSeconds >= 0 {
		s.accessTTLSeconds = accessTTLSeconds
	}
	if refreshTTL > 0 {
		s.refreshTTL = refreshTTL
	}
}

// Create 实现 go-oauth2 TokenStore。授权码保留在短期内存中；Access Token
// 只持久化 SHA-256，Refresh Token 只持久化代际，状态文件不保存原始令牌。
func (s *OAuthStore) Create(_ context.Context, info gooauth2.TokenInfo) error {
	if info == nil {
		return errors.New("OAuth token info is nil")
	}
	if code := strings.TrimSpace(info.GetCode()); code != "" && strings.TrimSpace(info.GetAccess()) == "" {
		return s.storeAuthorizationCode(code, info)
	}
	return s.storeGrantTokens(info)
}

func (s *OAuthStore) storeAuthorizationCode(raw string, info gooauth2.TokenInfo) error {
	resource := resourceFromTokenInfo(info)
	code := OAuthCode{
		ClientID:    strings.TrimSpace(info.GetClientID()),
		RedirectURI: strings.TrimSpace(info.GetRedirectURI()),
		Challenge:   strings.TrimSpace(info.GetCodeChallenge()),
		Resource:    resource,
		GrantID:     strings.TrimSpace(info.GetUserID()),
		ExpiresAt:   info.GetCodeCreateAt().Add(info.GetCodeExpiresIn()),
	}
	if code.ClientID == "" || code.RedirectURI == "" || code.Challenge == "" ||
		code.Resource == "" || code.GrantID == "" || !code.ExpiresAt.After(time.Now()) {
		return errors.New("OAuth authorization code metadata is incomplete")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredCodesLocked(time.Now())
	if _, exists := s.codes[raw]; exists {
		return errors.New("OAuth authorization code already exists")
	}
	if len(s.codes) >= maxOAuthCodes {
		return fmt.Errorf("OAuth authorization code limit %d reached", maxOAuthCodes)
	}
	// Code issuance follows successful owner consent. Persist promotion before
	// returning an authorization code; pending flooding cannot evict this client.
	if err := s.authorizeClientLocked(code.ClientID, time.Now().Unix()); err != nil {
		return err
	}
	s.codes[raw] = code
	return nil
}

func (s *OAuthStore) storeGrantTokens(info gooauth2.TokenInfo) error {
	clientID := strings.TrimSpace(info.GetClientID())
	grantID := strings.TrimSpace(info.GetUserID())
	resource := resourceFromTokenInfo(info)
	access := strings.TrimSpace(info.GetAccess())
	refresh := strings.TrimSpace(info.GetRefresh())
	if clientID == "" || grantID == "" || resource == "" || access == "" {
		return errors.New("OAuth access token metadata is incomplete")
	}

	var generation uint64
	if refresh != "" {
		claims, ok := s.verifyRefreshToken(refresh)
		if !ok || claims.GrantID != grantID {
			return oautherrors.ErrInvalidRefreshToken
		}
		generation = claims.Generation
	}

	accessIssuedAt := info.GetAccessCreateAt()
	accessNeverExpires := s.accessTTLSeconds == 0
	var accessExpiresAt int64
	if !accessNeverExpires {
		var ok bool
		accessExpiresAt, ok = unixAfterSeconds(accessIssuedAt, s.accessTTLSeconds)
		if !ok {
			return errors.New("OAuth access token lifetime overflows Unix time")
		}
	}
	grantExpiresAt := accessExpiresAt
	refreshIssuedAt := time.Time{}
	if refresh != "" {
		refreshIssuedAt = info.GetRefreshCreateAt()
		grantExpiresAt = refreshIssuedAt.Add(info.GetRefreshExpiresIn()).Unix()
	}
	if (!accessNeverExpires && accessExpiresAt <= time.Now().Unix()) || (grantExpiresAt != 0 && grantExpiresAt <= time.Now().Unix()) {
		return errors.New("OAuth token lifetime is already expired")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredGrantsLocked(time.Now().Unix())
	previous, exists := s.grants[grantID]
	if exists {
		if previous.Revoked || previous.ClientID != clientID || !EquivalentResourceURI(previous.Resource, resource) {
			return oautherrors.ErrInvalidGrant
		}
		if refresh != "" && generation != previous.CurrentGeneration+1 {
			return oautherrors.ErrInvalidRefreshToken
		}
	} else {
		if len(s.grants) >= maxOAuthGrants {
			return fmt.Errorf("OAuth grant limit %d reached", maxOAuthGrants)
		}
		if refresh != "" && generation != 1 {
			return oautherrors.ErrInvalidRefreshToken
		}
	}

	grant := OAuthGrant{
		ClientID:           clientID,
		Resource:           resource,
		AccessTokenHash:    hashOAuthToken(access),
		AccessIssuedAt:     accessIssuedAt.Unix(),
		AccessExpiresAt:    accessExpiresAt,
		AccessNeverExpires: accessNeverExpires,
		CurrentGeneration:  generation,
		ExpiresAt:          grantExpiresAt,
	}
	if !refreshIssuedAt.IsZero() {
		grant.RefreshIssuedAt = refreshIssuedAt.Unix()
	}
	previousClient := s.clients[clientID]
	authorizedClient, err := s.prepareAuthorizedClientLocked(clientID, time.Now().Unix())
	if err != nil {
		return err
	}
	s.clients[clientID] = authorizedClient

	delete(s.accessIndex, previous.AccessTokenHash)
	s.grants[grantID] = grant
	s.accessIndex[grant.AccessTokenHash] = grantID
	if err := s.persistStateLocked(); err != nil {
		s.clients[clientID] = previousClient
		delete(s.accessIndex, grant.AccessTokenHash)
		if exists {
			s.grants[grantID] = previous
			if previous.AccessTokenHash != "" {
				s.accessIndex[previous.AccessTokenHash] = grantID
			}
		} else {
			delete(s.grants, grantID)
		}
		return err
	}
	return nil
}

func (s *OAuthStore) RemoveByCode(_ context.Context, raw string) error {
	raw = strings.TrimSpace(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	code, ok := s.codes[raw]
	if !ok {
		return nil
	}
	code.Redeemed = true
	s.codes[raw] = code
	return nil
}

func (s *OAuthStore) RemoveByAccess(_ context.Context, raw string) error {
	return s.revokeByAccess(raw)
}

func (s *OAuthStore) RemoveByRefresh(_ context.Context, raw string) error {
	claims, ok := s.verifyRefreshToken(raw)
	if !ok {
		return nil
	}
	return s.RevokeGrant(claims.GrantID, s.refreshTTL)
}

func (s *OAuthStore) GetByCode(ctx context.Context, raw string) (gooauth2.TokenInfo, error) {
	raw = strings.TrimSpace(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredCodesLocked(time.Now())
	code, ok := s.codes[raw]
	if !ok {
		return nil, nil
	}
	if code.Redeemed {
		if err := s.revokeCodeFamilyLocked(code); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if resource := oauthResourceFromContext(ctx); resource != "" && !EquivalentResourceURI(code.Resource, resource) {
		return nil, nil
	}
	if clientID := oauthClientFromContext(ctx); clientID != "" && code.ClientID != clientID {
		return nil, nil
	}
	return tokenInfoForCode(raw, code), nil
}

func (s *OAuthStore) GetByAccess(ctx context.Context, raw string) (gooauth2.TokenInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	hash := hashOAuthToken(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	s.pruneExpiredGrantsLocked(now)
	grantID := s.accessIndex[hash]
	if grantID == "" {
		request := oauthRequestFromContext(ctx)
		if request.issuer == "" || request.resource == "" {
			return nil, nil
		}
		legacyGrantID, ok := validateLegacyAccessToken(raw, request.issuer, request.resource, s.signingKey)
		if !ok {
			return nil, nil
		}
		grantID = legacyGrantID
	}
	grant, ok := s.grants[grantID]
	if !ok || grant.Revoked {
		return nil, nil
	}
	if expected := oauthResourceFromContext(ctx); expected != "" && !EquivalentResourceURI(grant.Resource, expected) {
		return nil, nil
	}
	if clientID := oauthClientFromContext(ctx); clientID != "" && grant.ClientID != clientID {
		return nil, nil
	}
	if grant.AccessTokenHash != "" && grant.AccessTokenHash != hash {
		return nil, nil
	}
	if !grant.AccessNeverExpires && (grant.AccessExpiresAt == 0 || grant.AccessExpiresAt <= now) {
		return nil, nil
	}
	return tokenInfoForGrant(grantID, grant, raw, "", s.accessTTLSeconds, s.refreshTTL), nil
}

func (s *OAuthStore) GetByRefresh(ctx context.Context, raw string) (gooauth2.TokenInfo, error) {
	claims, ok := s.verifyRefreshToken(raw)
	if !ok {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	s.pruneExpiredGrantsLocked(now)
	grant, ok := s.grants[claims.GrantID]
	if !ok || grant.Revoked || grant.ExpiresAt <= now {
		return nil, nil
	}
	if expected := oauthResourceFromContext(ctx); expected != "" && !EquivalentResourceURI(grant.Resource, expected) {
		return nil, nil
	}
	if clientID := oauthClientFromContext(ctx); clientID != "" && grant.ClientID != clientID {
		return nil, nil
	}
	if claims.Generation != grant.CurrentGeneration {
		if claims.Generation < grant.CurrentGeneration {
			grant.Revoked = true
			delete(s.accessIndex, grant.AccessTokenHash)
			s.grants[claims.GrantID] = grant
			if err := s.persistStateLocked(); err != nil {
				grant.Revoked = false
				s.grants[claims.GrantID] = grant
				if grant.AccessTokenHash != "" {
					s.accessIndex[grant.AccessTokenHash] = claims.GrantID
				}
				return nil, err
			}
		}
		return nil, nil
	}
	return tokenInfoForGrant(claims.GrantID, grant, "", raw, s.accessTTLSeconds, s.refreshTTL), nil
}

func (s *OAuthStore) revokeByAccess(raw string) error {
	hash := hashOAuthToken(strings.TrimSpace(raw))
	s.mu.Lock()
	grantID := s.accessIndex[hash]
	s.mu.Unlock()
	if grantID == "" {
		return nil
	}
	return s.RevokeGrant(grantID, s.refreshTTL)
}

func (s *OAuthStore) revokeCodeFamilyLocked(code OAuthCode) error {
	now := time.Now()
	previous, existed := s.grants[code.GrantID]
	grant := previous
	grant.ClientID = code.ClientID
	grant.Resource = code.Resource
	grant.Revoked = true
	if grant.ExpiresAt < now.Add(s.refreshTTL).Unix() {
		grant.ExpiresAt = now.Add(s.refreshTTL).Unix()
	}
	delete(s.accessIndex, previous.AccessTokenHash)
	s.grants[code.GrantID] = grant
	if err := s.persistStateLocked(); err != nil {
		if existed {
			s.grants[code.GrantID] = previous
			if previous.AccessTokenHash != "" {
				s.accessIndex[previous.AccessTokenHash] = code.GrantID
			}
		} else {
			delete(s.grants, code.GrantID)
		}
		return err
	}
	return nil
}

func tokenInfoForCode(raw string, code OAuthCode) gooauth2.TokenInfo {
	info := models.NewToken()
	info.SetClientID(code.ClientID)
	info.SetUserID(code.GrantID)
	info.SetRedirectURI(code.RedirectURI)
	info.SetCode(raw)
	info.SetCodeCreateAt(code.ExpiresAt.Add(-5 * time.Minute))
	info.SetCodeExpiresIn(5 * time.Minute)
	info.SetCodeChallenge(code.Challenge)
	info.SetCodeChallengeMethod(gooauth2.CodeChallengeS256)
	info.SetExtension(url.Values{oauthResourceExtension: []string{code.Resource}})
	return info
}

func tokenInfoForGrant(grantID string, grant OAuthGrant, access, refresh string, accessTTLSeconds int64, refreshTTL time.Duration) gooauth2.TokenInfo {
	info := models.NewToken()
	info.SetClientID(grant.ClientID)
	info.SetUserID(grantID)
	info.SetAccess(access)
	info.SetRefresh(refresh)
	info.SetExtension(url.Values{oauthResourceExtension: []string{grant.Resource}})

	accessIssuedAt := time.Unix(grant.AccessIssuedAt, 0)
	if grant.AccessIssuedAt == 0 {
		accessIssuedAt = time.Now()
	}
	info.SetAccessCreateAt(accessIssuedAt)
	if grant.AccessNeverExpires {
		info.SetAccessExpiresIn(0)
	} else if grant.AccessExpiresAt > grant.AccessIssuedAt {
		info.SetAccessExpiresIn(oauthFrameworkTTL(grant.AccessExpiresAt - grant.AccessIssuedAt))
	} else {
		info.SetAccessExpiresIn(oauthFrameworkTTL(accessTTLSeconds))
	}

	if refresh != "" {
		refreshIssuedAt := time.Unix(grant.RefreshIssuedAt, 0)
		if grant.RefreshIssuedAt == 0 {
			refreshIssuedAt = time.Unix(grant.ExpiresAt, 0).Add(-refreshTTL)
		}
		info.SetRefreshCreateAt(refreshIssuedAt)
		info.SetRefreshExpiresIn(time.Unix(grant.ExpiresAt, 0).Sub(refreshIssuedAt))
	}
	return info
}

func unixAfterSeconds(start time.Time, seconds int64) (int64, bool) {
	if seconds <= 0 || start.Unix() > (1<<63-1)-seconds {
		return 0, false
	}
	return start.Unix() + seconds, true
}

func resourceFromTokenInfo(info gooauth2.TokenInfo) string {
	extension, ok := info.(gooauth2.ExtendableTokenInfo)
	if !ok {
		return ""
	}
	return strings.TrimSpace(extension.GetExtension().Get(oauthResourceExtension))
}

func hashOAuthToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type oauthRequestContext struct {
	issuer   string
	resource string
	clientID string
}

type oauthRequestContextKey struct{}

func WithOAuthRequest(ctx context.Context, issuer, resource, clientID string) context.Context {
	return context.WithValue(ctx, oauthRequestContextKey{}, oauthRequestContext{
		issuer:   strings.TrimRight(strings.TrimSpace(issuer), "/"),
		resource: strings.TrimSpace(resource),
		clientID: strings.TrimSpace(clientID),
	})
}

func oauthRequestFromContext(ctx context.Context) oauthRequestContext {
	value, _ := ctx.Value(oauthRequestContextKey{}).(oauthRequestContext)
	return value
}

func oauthResourceFromContext(ctx context.Context) string {
	return oauthRequestFromContext(ctx).resource
}

func oauthClientFromContext(ctx context.Context) string {
	return oauthRequestFromContext(ctx).clientID
}

var _ gooauth2.TokenStore = (*OAuthStore)(nil)
