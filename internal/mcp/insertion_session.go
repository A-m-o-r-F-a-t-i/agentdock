package mcp

import (
	"context"
	"encoding/json"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/insertion"
)

const insertionCapability = "agentdock/response-additions-v1"
const insertionHostReceipts = "agentdock/response-additions-receipts-v1"

// Initialize advertises capability. Per-request metadata supplies correlation
// and receipts. Tool arguments and third-party output never enter this parser.
func (s *Server) requestInsertionSession(ctx context.Context, request *mcpsdk.CallToolRequest) (context.Context, error) {
	host := insertion.Transport{}
	if request.Session != nil {
		if initialized := request.Session.InitializeParams(); initialized != nil && initialized.Capabilities != nil {
			if raw, ok := initialized.Capabilities.Experimental[insertionCapability]; ok {
				data, err := json.Marshal(raw)
				if err != nil || len(data) > 1024 {
					return ctx, errors.New("invalid initialized insertion capability")
				}
				if err = json.Unmarshal(data, &host); err != nil {
					return ctx, errors.New("invalid initialized insertion capability")
				}
				if host.HostType == "" && initialized.ClientInfo != nil {
					host.HostType = initialized.ClientInfo.Name
				}
			}
		}
	}
	// An outer ID is per invocation, never a session-wide reusable identity.
	host.OuterCallID = ""
	if raw, ok := request.Params.Meta[insertionCapability]; ok {
		data, err := json.Marshal(raw)
		if err != nil || len(data) > 1024 {
			return ctx, errors.New("invalid insertion request metadata")
		}
		var correlation struct {
			OuterCallID string `json:"outer_call_id"`
			Passthrough *bool  `json:"passthrough,omitempty"`
		}
		if err = json.Unmarshal(data, &correlation); err != nil {
			return ctx, errors.New("invalid insertion correlation")
		}
		host.OuterCallID = correlation.OuterCallID
		if correlation.Passthrough != nil && !*correlation.Passthrough {
			host.Passthrough = false
		}
	}
	if len(host.HostType) > 80 || len(host.OuterCallID) > 160 {
		return ctx, errors.New("insertion host identity exceeds limit")
	}
	host.Passthrough = host.Passthrough && host.OuterCallID != "" && host.HostType != ""
	host.ContextAcknowledgement = host.ContextAcknowledgement && host.Passthrough
	ctx = app.WithInsertionTransport(ctx, host)
	if raw, ok := request.Params.Meta[insertionHostReceipts]; ok {
		data, err := json.Marshal(raw)
		if err != nil || len(data) > 4096 {
			return ctx, errors.New("insertion host receipts exceed limit")
		}
		var input struct {
			Stage       string              `json:"stage"`
			OuterCallID string              `json:"outer_call_id"`
			Receipts    []insertion.Receipt `json:"receipts"`
		}
		if err = json.Unmarshal(data, &input); err != nil {
			return ctx, errors.New("invalid insertion host receipt metadata")
		}
		if err = s.runtime.RecordInsertionTransportReceipt(ctx, input.Stage, input.OuterCallID, input.Receipts); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}
