package httpx

import (
	"context"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/permission"
	"testing"
)

func newHTTPUnrestrictedRuntime(t *testing.T, cfg config.Config) (*app.Runtime, error) {
	t.Helper()
	r, err := app.NewRuntime(cfg)
	if err != nil {
		return nil, err
	}
	_, err = r.RuntimePermissionsUpdate(context.Background(), permission.Change{Scope: "global", Mode: permission.Full, ExpectedRevision: 1, ConfirmFull: true})
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}
