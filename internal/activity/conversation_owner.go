package activity

import "context"

// LocalTarget returns the canonical owner only to the authenticated direct-local
// management boundary. An arbitrary conversation id never grants this authority.
func (r *ConversationRegistry) LocalTarget(ctx context.Context, id string) (Conversation, string, error) {
	var conversation Conversation
	var owner string
	if !IsLocalManagement(ctx) {
		return conversation, owner, ErrConversationOwner
	}
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		record, found := state.Items[id]
		if !found {
			return false, ErrConversationNotFound
		}
		if record.DeletedAt != nil {
			return false, ErrConversationDeleted
		}
		conversation, owner = record.Conversation, record.OwnerKey
		return false, nil
	})
	return conversation, owner, err
}
