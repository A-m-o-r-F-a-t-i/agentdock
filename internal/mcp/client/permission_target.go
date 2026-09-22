package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type approvedToolTargetKey struct{}

// WithApprovedToolTarget fixes the installed server definition that the user
// reviewed. The fingerprint is private and never replaces authentication.
func WithApprovedToolTarget(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, approvedToolTargetKey{}, fingerprint)
}
func definitionFingerprint(definition ServerConfig) (string, error) {
	definition.Description = ""
	raw, err := json.Marshal(struct {
		Config                                 ServerConfig
		PluginRoot, PluginData, PluginVersion  string
		PackageEnv, PackageHeaders, RuntimeEnv map[string]string
	}{definition, definition.PluginRoot, definition.PluginData, definition.PluginVersion, definition.PackageEnv, definition.PackageHeaders, definition.RuntimeEnv})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
func (m *Manager) PermissionTargetFingerprint(ctx context.Context, qualifiedName string) (string, error) {
	if err := m.syncRegistry(); err != nil {
		return "", err
	}
	server, _, err := splitQualifiedToolName(qualifiedName)
	if err != nil {
		return "", err
	}
	definition, _, release, err := m.lockServer(server)
	if err != nil {
		return "", err
	}
	defer release()
	definition, err = m.runtimeConfig(definition)
	if err != nil {
		return "", err
	}
	return definitionFingerprint(definition)
}
func checkApprovedToolTarget(ctx context.Context, definition ServerConfig) error {
	expected, _ := ctx.Value(approvedToolTargetKey{}).(string)
	if expected == "" {
		return nil
	}
	actual, err := definitionFingerprint(definition)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("MCP server configuration changed after approval; the fixed request was not dispatched")
	}
	return nil
}
