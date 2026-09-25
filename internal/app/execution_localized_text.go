package app

import (
	"github.com/uvwt/agentdock/internal/activity"
	"regexp"
)

var ownedPermissionSummary = regexp.MustCompile(`^scope=(global|workspace|conversation) scope_id=([A-Za-z0-9_-]*) mode=([a-z_]+) revision=([0-9]+)；操作系统权限未改变。$`)

func describeOwnedManagement(event activity.Event) activity.Event {
	if event.ToolName != "permission.update" {
		return event
	}
	if event.Title == "修改执行权限" {
		event.TitleText = activity.NewLocalizedText("permission.update", event.Title)
	}
	if event.Status == "succeeded" {
		if match := ownedPermissionSummary.FindStringSubmatch(event.Summary); match != nil {
			event.SummaryText = activity.NewLocalizedText("permission.updated", event.Summary, match[1], match[2], match[3], match[4])
		}
	}
	return event
}
