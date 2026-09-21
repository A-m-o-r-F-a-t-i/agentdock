package activity

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const ConversationTerminatedMessage = "用户已终止此对话，请停止执行。"

var ErrConversationTerminated = errors.New(ConversationTerminatedMessage)
var ErrConversationDeleted = errors.New("conversation was permanently deleted")

func titlePriority(source string) int {
	switch source {
	case "manual":
		return 5
	case "host":
		return 4
	case "task":
		return 3
	case "operation":
		return 2
	case "fallback":
		return 1
	}
	return 0
}
func applyAutomaticTitle(item *Conversation, title, source string) bool {
	title = strings.TrimSpace(title)
	if title == "" || title == "新对话" || len(title) > 512 || !utf8.ValidString(title) || strings.ContainsAny(title, "\r\n\x00") {
		return false
	}
	existing := item.TitleSource
	// Pre-1.1.3 custom titles have no source marker: preserve them as manual.
	if existing == "" && item.Title != "" && item.Title != "新对话" {
		existing = "manual"
	}
	if titlePriority(source) < titlePriority(existing) || titlePriority(source) == titlePriority(existing) && !(source == "fallback" && strings.HasPrefix(item.Title, "对话 · ") && !strings.HasPrefix(title, "对话 · ")) {
		return false
	}
	item.Title, item.TitleSource = title, source
	return true
}

// AutoName only upgrades the title's provenance. Equal-priority task/operation
// names retain the first meaningful value and can never replace a manual name.
func (r *ConversationRegistry) AutoName(ctx context.Context, id, title, source string) error {
	if id == "" {
		return nil
	}
	return r.state(ctx, func(state *conversationState) (bool, error) {
		record, ok := state.Items[id]
		if !ok || record.DeletedAt != nil {
			return false, ErrConversationNotFound
		}
		if !applyAutomaticTitle(&record.Conversation, title, source) {
			return false, nil
		}
		state.Items[id] = record
		return true, nil
	})
}

// SetTerminated is a durable local-user gate, not a model-callable operation.
// It does not alter immutable call bindings or automatically replay anything.
func (r *ConversationRegistry) SetTerminated(ctx context.Context, id string, terminated bool) (Conversation, error) {
	if !IsLocalManagement(ctx) {
		return Conversation{}, ErrConversationOwner
	}
	var result Conversation
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		record, ok := state.Items[id]
		if !ok || record.DeletedAt != nil {
			return false, ErrConversationNotFound
		}
		if !terminated && record.TrashedAt != nil {
			return false, ErrConversationTrashed
		}
		now := time.Now().UTC()
		if terminated {
			if record.TerminatedAt == nil {
				record.TerminatedAt = &now
			}
		} else {
			record.TerminatedAt = nil
		}
		record.UpdatedAt = now
		state.Items[id] = record
		result = cloneConversation(record.Conversation)
		return true, nil
	})
	return result, err
}

// IsDeleted includes source tombstones omitted from navigation and ordinary Get.
func (r *ConversationRegistry) IsDeleted(ctx context.Context, id string) (bool, error) {
	deleted := false
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		deleted = state.Items[id].DeletedAt != nil
		return false, nil
	})
	return deleted, err
}
