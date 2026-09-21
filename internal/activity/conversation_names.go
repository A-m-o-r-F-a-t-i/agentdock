package activity

import (
	"context"
	"errors"
)

// AutomaticTitle is a proposed display name, never an identity or permission.
type AutomaticTitle struct {
	Title  string
	Source string
}

// AutoNameBatch reads and writes the registry once. Recheck title priority under
// the storage lock so a concurrent manual rename or deletion always wins over
// an older list snapshot. Missing/deleted objects are omitted from the result.
func (r *ConversationRegistry) AutoNameBatch(ctx context.Context, proposals map[string]AutomaticTitle) (map[string]Conversation, error) {
	result := make(map[string]Conversation, len(proposals))
	if len(proposals) == 0 {
		return result, nil
	}
	if len(proposals) > 20000 {
		return nil, errors.New("too many automatic conversation titles")
	}
	for id, proposal := range proposals {
		if !conversationIdentifier.MatchString(id) || proposal.Source != "fallback" && proposal.Source != "operation" && proposal.Source != "task" && proposal.Source != "host" {
			return nil, errors.New("invalid automatic conversation title proposal")
		}
	}
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		dirty := false
		for id, proposal := range proposals {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			record, found := state.Items[id]
			if !found || record.DeletedAt != nil {
				continue
			}
			if applyAutomaticTitle(&record.Conversation, proposal.Title, proposal.Source) {
				state.Items[id] = record
				dirty = true
			}
			result[id] = cloneConversation(record.Conversation)
		}
		return dirty, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
