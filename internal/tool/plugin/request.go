package plugin

import "github.com/uvwt/agentdock/internal/activity"

type ManageRequest struct {
	activity.Binding
	Action     string `json:"action"`
	Name       string `json:"name,omitempty"`
	Source     string `json:"source,omitempty"`
	MemberType string `json:"member_type,omitempty"`
	Member     string `json:"member,omitempty"`
}

type LoadRequest struct {
	Name string `json:"name"`
}
