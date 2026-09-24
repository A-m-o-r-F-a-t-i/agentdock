package client

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
)

// Tokens are transport-owned and unique within this client session. A server
// notification can reach only the callback registered for that exact request.
func (c *sdkProtocolClient) registerProgress(ctx context.Context, name string, params *mcpsdk.CallToolParams) func() {
	sink := activity.ProgressSinkFromContext(ctx)
	if sink == nil {
		return func() {}
	}
	token := fmt.Sprintf("agentdock-progress-%d", c.progressSequence.Add(1))
	c.progressMu.Lock()
	if c.progressSinks == nil {
		c.progressSinks = map[string]activity.ProgressSink{}
	}
	c.progressSinks[token] = func(value activity.Progress) { value.Server = c.cfg.Name; value.Tool = name; sink(value) }
	c.progressMu.Unlock()
	params.SetProgressToken(token)
	return func() { c.progressMu.Lock(); delete(c.progressSinks, token); c.progressMu.Unlock() }
}
func (c *sdkProtocolClient) receiveProgress(_ context.Context, request *mcpsdk.ProgressNotificationClientRequest) {
	if request == nil || request.Params == nil {
		return
	}
	params := request.Params
	token, ok := params.ProgressToken.(string)
	if !ok {
		return
	}
	c.progressMu.Lock()
	sink := c.progressSinks[token]
	c.progressMu.Unlock()
	if sink != nil {
		sink(activity.Progress{Message: params.Message, Current: params.Progress, Total: params.Total})
	}
}
