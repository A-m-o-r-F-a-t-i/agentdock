package app

import (
	"context"
	"regexp"
	"strings"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
	toolfile "github.com/uvwt/agentdock/internal/tool/file"
)

type outputPolicyContextKey struct{}

var outputSourceURI = regexp.MustCompile(`^activity://call/(call_[a-f0-9]{32})/source$`)

// An activity source is an existing immutable payload reached through its
// owning call, never a filesystem path or a blob hash supplied by the caller.
func (r *Runtime) readFileWithOutputSource(ctx context.Context, request toolfile.ReadRequest) (Result, error) {
	if !strings.HasPrefix(request.Path, "activity://") {
		if request.Offset != nil || request.LimitChars != nil {
			return nil, toolError("INVALID_ARGUMENT", "offset and limit_chars apply only to host-issued activity source references.", "validation")
		}
		return r.files.ReadFile(ctx, request)
	}
	parts := outputSourceURI.FindStringSubmatch(request.Path)
	if len(parts) != 2 || request.StartLine != nil || request.EndLine != nil || request.MaxBytes != nil {
		return nil, toolError("INVALID_ARGUMENT", "Use an exact activity source URI with offset and limit_chars only.", "validation")
	}
	call, err := r.activity.Call(ctx, parts[1])
	if err != nil {
		return nil, err
	}
	bound := activity.FromContext(ctx)
	if !activity.IsLocalManagement(ctx) {
		if call.SourceOwnerKey == "" || call.SourceOwnerKey != activity.SourceOwnerKey(ctx) {
			return nil, toolError("OUTPUT_OWNER_MISMATCH", "The retained output belongs to another authenticated client.", "permission")
		}
		if call.ConversationID != "" {
			if err = r.conversations.Owns(ctx, call.ConversationID); err != nil {
				return nil, err
			}
			if err = r.checkConversationGate(ctx, call.ConversationID); err != nil {
				return nil, err
			}
			if bound.ConversationID != call.ConversationID && (bound.TaskID == "" || bound.TaskID != call.TaskID) {
				return nil, toolError("OUTPUT_CONVERSATION_MISMATCH", "Resume the owning task before reading output from another conversation.", "permission")
			}
		}
		if call.WorkspaceID != "" && bound.WorkspaceID != call.WorkspaceID {
			return nil, toolError("OUTPUT_WORKSPACE_MISMATCH", "Select the original workspace before reading this retained output.", "permission")
		}
	}
	if call.OutputSource == nil || call.OutputSource.Ref == "" {
		return nil, toolError("OUTPUT_NOT_STORED", "This call has no retained ordinary-output source.", "runtime")
	}
	policy, ok := ctx.Value(outputPolicyContextKey{}).(config.ToolOutputSettings)
	if !ok {
		policy = r.MCPPresentationSettings().ToolOutput
	}
	limit := 100000
	if policy.Enabled {
		limit = policy.MaxChars
	}
	if request.LimitChars != nil {
		limit = min(limit, *request.LimitChars)
	}
	offset := int64(0)
	if request.Offset != nil {
		offset = *request.Offset
	}
	page, err := r.activity.ReadCallPayloadCharacters(ctx, parts[1], "source", offset, limit)
	if err != nil {
		return nil, err
	}
	descriptor := *page.Payload
	descriptor.Preview = ""
	return Result{"path": request.Path, "content": page.Text, "encoding": "utf-8", "size_bytes": page.Payload.Bytes, "offset": page.Offset, "next_offset": page.NextOffset, "has_more": page.HasMore, "unit": "unicode_scalar", "limit_chars": limit, "returned_chars": page.ReturnedChars, "source_state": page.Payload.State, "payload": descriptor}, nil
}
