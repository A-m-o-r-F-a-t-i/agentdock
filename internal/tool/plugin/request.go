package plugin

import "github.com/uvwt/agentdock/internal/activity"

type ManageRequest struct {
	SourceType            string `json:"source_type,omitempty"`
	SourceAdapter         string `json:"source_adapter,omitempty"`
	SourceVersion         string `json:"source_version,omitempty"`
	GitRef                string `json:"git_ref,omitempty"`
	GitCommit             string `json:"git_commit,omitempty"`
	Subdir                string `json:"subdir,omitempty"`
	SHA256                string `json:"sha256,omitempty"`
	Catalog               string `json:"catalog,omitempty"`
	CatalogItem           string `json:"catalog_item,omitempty"`
	Enabled               *bool  `json:"enabled,omitempty"`
	Confirmed             bool   `json:"confirmed,omitempty"`
	ConfirmedSourceChange bool   `json:"confirmed_source_change,omitempty"`

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
