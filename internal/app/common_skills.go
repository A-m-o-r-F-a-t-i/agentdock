package app

import (
	"context"
	"os"
	"path/filepath"

	toolskill "github.com/uvwt/agentdock/internal/tool/skill"
)

func commonSkillCapabilityIndex() (*capabilityCommonSkillIndex, error) {
	return commonSkillCapabilityIndexContext(context.Background(), nil)
}

func commonSkillCapabilityIndexContext(ctx context.Context, watch func(string)) (*capabilityCommonSkillIndex, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".agents", "skills")
	index, err := scanCommonFilesystemSkillsContext(ctx, root, watch)
	if err != nil {
		return nil, err
	}
	items := make([]capabilityCommonSkillItem, 0, len(index.Items))
	for _, item := range index.Items {
		skillRef := toolskill.SharedSkillRef(item.Name)
		items = append(items, capabilityCommonSkillItem{
			Name: item.Name, Description: item.Description,
			File: skillRef + "/SKILL.md", SkillRef: skillRef,
			SourceType: "shared", SourceID: "global",
		})
	}
	return &capabilityCommonSkillIndex{Root: root, Total: index.Total, Truncated: index.Truncated, Items: items}, nil
}
