package mcp

import (
	"context"
	"fmt"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
)

func (s *Server) observeDiscovery(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
		// The SDK rejects an unknown tool before its registered handler runs.
		// Retain that failed attempt at ingress while leaving protocol errors to SDK.
		if method == "tools/call" {
			if params, ok := request.GetParams().(*mcpsdk.CallToolParamsRaw); ok && params != nil {
				if _, known := s.runtime.ToolDefinition(params.Name); !known {
					origin, err := requestConversationContext(ctx, params.Meta)
					message := "unknown tool"
					if err != nil {
						message = err.Error()
					}
					_, _ = s.runtime.RejectToolCall(origin, params.Name, message)
				}
			}
			return next(ctx, method, request)
		}
		switch method {
		case "tools/list", "resources/list", "resources/read", "prompts/list", "prompts/get":
		default:
			return next(ctx, method, request)
		}
		if params := request.GetParams(); params != nil {
			if host, ok := params.GetMeta()["openai/session"].(string); ok && host != "" && len(host) <= 1024 {
				source := activity.SourceFromContext(ctx)
				source.Provider = "openai"
				source.HostConversationID = host
				ctx = activity.WithSource(ctx, source)
			}
		}
		var result mcpsdk.Result
		_, err := s.runtime.ObserveInternal(activity.WithDiagnostic(ctx), method, "协议发现 · "+method, func(observed context.Context) (app.Result, error) {
			var err error
			result, err = next(observed, method, request)
			summary := app.Result{}
			if listed, ok := result.(*mcpsdk.ListToolsResult); ok {
				summary["count"] = fmt.Sprint(len(listed.Tools))
			}
			return summary, err
		})
		return result, err
	}
}
