package app

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/agentinstructions"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/contextguide"
	pluginregistry "github.com/uvwt/agentdock/internal/plugin"
	"github.com/uvwt/agentdock/internal/taskstate"
	tooltask "github.com/uvwt/agentdock/internal/tool/task"
	"github.com/uvwt/agentdock/internal/workspace"
)

func (r *Runtime) AgentDockContext(ctx context.Context) (Result, error) {
	return r.Call(ctx, "agentdock_context", map[string]any{})
}

// AgentDockLocalContext 仅供 Nexus Bridge 使用。它不读取 Nexus 统一管理的
// Workflow/Recall，避免 fleet 聚合时按节点重复回灌共享上下文。
func (r *Runtime) AgentDockLocalContext(ctx context.Context) (Result, error) {
	return r.agentDockContext(ctx, true, "")
}

func (r *Runtime) agentDockContext(ctx context.Context, nexusLocalOnly bool, workdir string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.ContextBudget())
	defer cancel()
	started := time.Now()
	ruleStarted := time.Now()

	instructions, err := r.InstructionFiles(ctx, workdir)
	if err != nil {
		return nil, err
	}
	ruleElapsed := time.Since(ruleStarted)
	pluginStarted := time.Now()
	directory, pluginInfo, directoryErr := r.pluginStore.Snapshot(ctx)
	if directoryErr != nil {
		return nil, toolErrorDetails("CONTEXT_PREPARATION_FAILED", directoryErr.Error(), "runtime", map[string]any{"stage": "plugins", "budget_ms": r.cfg.ContextBudget().Milliseconds()})
	}
	pluginElapsed := time.Since(pluginStarted)
	skillStarted := time.Now()
	skills, skillInfo, skillErr := r.contextSkillIndex(ctx, directory, nexusLocalOnly)
	skillElapsed := time.Since(skillStarted)
	mcpStarted := time.Now()

	dynamicMCP, dynamicMCPErr := r.dynamicMCPCapabilityIndexContext(ctx, nexusLocalOnly, directory)
	mcpElapsed := time.Since(mcpStarted)
	plugins := []capabilityPluginItem{}
	if !nexusLocalOnly {
		plugins = pluginCapabilityIndexFromDirectory(directory)
	}
	commonStarted := time.Now()
	commonSkills, commonInfo, commonSkillErr := r.cachedCommonSkillIndex(ctx)
	commonElapsed := time.Since(commonStarted)
	var taskElapsed time.Duration
	contextResult := capabilityContext{
		Skills:            skills,
		CommonSkills:      commonSkills,
		Plugins:           plugins,
		DynamicMCP:        dynamicMCP,
		WorkflowTemplates: []capabilityTemplateItem{},
		Rules: []string{
			contextguide.Reuse,
			"需要真实执行命令或检查环境时，先用 exec_command 查看现状，再修改，修改后真实验证。",
			"先根据 Skill 索引的 name、description 和来源选择相关 Skill，再用 read_file 读取宿主返回的 file；需要绑定命令时直接使用宿主返回的 skill_ref，不自行按名称拼接或重新解析。",
			"workspace_skills、skills 和 common_skills 中的同名项是不同来源候选，不静默覆盖；当前项目通常优先考虑 workspace Skill，但必须使用所选候选自己的 skill_ref/file。若 common_skills.truncated=true 且当前索引未命中，可 list_dir 查看 common_skills.root 后再通过 workspace/共享 Skill 索引取得精确引用。",
			"AgentDock 自带工具直接调用；动态 MCP 工具先用 mcp_tool_search 查找、mcp_tool_inspect 读取 schema，再用 mcp_tool_call 执行。",
			"已取得本项目规则时直接继续操作，不另做 workspace_context；仅工作区规则或项目级 Skill 作用域变化时定向刷新。",
		},
	}
	if r.cfg.InstructionsFile == "" && strings.TrimSpace(r.cfg.Instructions) != "" {
		contextResult.Rules = append(contextResult.Rules, "Additional operator instructions:\n"+r.cfg.Instructions)
	}
	if nexusLocalOnly {
		// Keep the shared Bridge context contract unchanged. Device guidance travels
		// through its existing rules field, not a node-specific schema extension.
		if text := instructions.Text(); text != "" {
			contextResult.Rules = append(contextResult.Rules, text)
		}
	} else {
		contextResult.InstructionFiles = &instructions
		contextResult.Rules = append(contextResult.Rules, InsertionInstructions, "托管 MCP 简介可用 mcp_manage inspect → update/reset_override 修改宿主覆盖，携带 expected_revision 与 scope。版本和工具数量以当前发现事实为准；能力更新提示后重读目标工具 schema，不展开无关 Heavy 插件。")
		contextResult.Rules = append(contextResult.Rules,
			"plugins 仅列出 Heavy 插件摘要。命中后调用 plugin_load(name) 展开成员；普通插件的已启用 Skill/MCP 直接显示在顶层 skills/dynamic_mcp。",
			"instruction_files.files 已自动载入规则正文；只应用 status=loaded 的条目，按全局、项目根目录、子目录顺序处理。项目规则不得削弱全局安全要求。操作其他工作区或规则文件已改变时，先调用 agentdock_context 并传入对应 workdir 刷新；该参数不会修改命令的默认工作目录。",
		)
	}
	if !nexusLocalOnly {
		// runtime 只保留模型操作主机所需的稳定环境事实；Nexus Bridge 已通过 Hello 持有这些节点事实，
		// 私有 context.local 不重复传输，避免两个来源长期漂移。
		contextResult.Runtime = &capabilityRuntimeContext{
			Version: buildinfo.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
			AgentDockHome: r.cfg.AgentDockHome, AgentDockDefaultDir: r.cfg.AgentDockDefaultDir,
			DefaultCWD: r.ws.DefaultDisplay(), PathModel: config.PathModel,
		}
		taskStarted := time.Now()
		indexCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		index, indexErr := r.taskTools.ContextIndex(indexCtx)
		cancel()
		taskElapsed = time.Since(taskStarted)
		contextResult.Tasks = &index
		selectedWorkspace, workspaceErr := r.workspaceRegistry.Select(ctx, "", "")
		if strings.TrimSpace(workdir) != "" {
			resolved, resolveErr := r.ws.ResolveExisting(workdir)
			if resolveErr != nil {
				workspaceErr = resolveErr
			} else {
				selectedWorkspace, workspaceErr = r.workspaceRegistry.EnsureRoot(ctx, resolved.Abs)
			}
		}
		if workspaceErr == nil {
			contextResult.Workspace = &selectedWorkspace
		} else {
			contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "workspace", Message: "工作区注册信息暂不可用。写入前调用 workspace_manage 明确选择项目，不能回退到未知当前目录。"})
		}
		if indexErr != nil {
			contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "tasks", Message: "任务索引暂不可用；现有能力仍可使用，请检查任务存储。"})
		}
		contextResult.Rules = append(contextResult.Rules, "恢复任务时使用 tasks 索引中的 task_id 调用 task_manage resume 或 set_current 一次；后续普通工具自动继承服务端任务和线程绑定，无需重复填写 task_id/thread_id。对话身份由接入层解析，不能通过业务参数指定。运行中的命令继续观察原 session_id。多候选无法区分时只返回候选摘要，不创建重复任务。")
		contextResult.Rules = append(contextResult.Rules, "新任务传入本次 workspace.workspace_id；恢复任务优先使用其线程工作区。源码用 source，交付物用 artifact，临时文件用 scratch，缓存用 cache。工作区外单次目标须显式传 target_kind=external 与 external_path。注册或修订项目使用 workspace_manage；工作区路由不构成命令沙箱。")
	}
	if skillErr != nil {
		contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "skills", Message: "Skill 索引暂不可用。"})
	}
	if dynamicMCPErr != nil {
		contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "dynamic_mcp", Message: "动态 MCP 索引暂不可用。"})
	}
	if commonSkillErr != nil {
		contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "common_skills", Message: "通用 Skill 索引暂不可用；需要时可直接检查 ~/.agents/skills。"})
	}

	if requiresACP(r.cfg) {
		profiles := make([]capabilityACPProfileContext, 0, len(r.cfg.EffectiveACPProfiles()))
		for _, profile := range r.cfg.EffectiveACPProfiles() {
			profiles = append(profiles, capabilityACPProfileContext{ID: profile.ID, Kind: profile.Kind})
		}
		contextResult.ACP = &capabilityACPContext{
			Enabled:        true,
			DefaultProfile: r.cfg.EffectiveACPDefaultProfile(),
			Profiles:       profiles,
			Description: "本机 Coding Agent 通道（Agent Client Protocol）。仅当用户明确要求时使用，可用来获取独特见解与编排任务；" +
				"不是动态 MCP，不要用 mcp_tool_*。",
		}
	}

	if requiresNexus(r.cfg) && !nexusLocalOnly {
		templates, templateErr := r.templateCapabilityIndex(ctx)
		if templateErr != nil {
			contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "workflow_templates", Message: "工作流模板索引暂不可用；多步骤任务仍应先 workflow_template_manage match。"})
		}
		memoryItems, memoryErr := r.memoryCapabilityIndex(ctx)
		if memoryErr != nil {
			contextResult.Warnings = append(contextResult.Warnings, capabilityWarning{Source: "recall", Message: "记忆精简摘要暂不可用；需要项目事实时调用 recall_search/recall_read 精确确认。"})
		}
		contextResult.WorkflowTemplates = templates
		contextResult.Recall = &capabilityRecallContext{Enabled: true, Items: memoryItems}
		contextResult.Rules = append(contextResult.Rules,
			"涉及多步骤开发、部署、排障、迁移、Docker、VPS 或 Git 提交推送时，先 workflow_template_manage match；无合适模板时创建普通可恢复任务。",
			"当多个工作流模板同时适合当前任务时，调用 workflow_template_manage get_many 读取详情；模型必须结合用户目标裁剪、去重、排序并生成最终 steps 和 completion_conditions，再用 source_template_ids 创建任务，服务端不会自动拼接模板。",
			"普通项目记忆走 recall_*；private_note_manage 只在用户明确要求私密笔记，或内容明显包含 secret、凭据、个人敏感信息时使用。私密检索只返回名称、简介、标签、分类和路径等元数据；正文必须显式 read，Git 只备份 age 密文。",
		)
	}

	contextResult.Rules = append(contextResult.Rules, "任务执行过程中，在形成有恢复价值的断点时调用 task_manage checkpoint；可用 completed_step_ids/current_step_id 原子批量更新，final_review=pass 不会自动补全未完成步骤。")
	if requiresNexus(r.cfg) && !nexusLocalOnly {
		contextResult.Rules = append(contextResult.Rules,
			"记忆启动索引只提供紧凑背景与资料入口；索引已给出具体 path 时优先 recall_read 该条目，只有索引未覆盖且任务依赖具体历史事实时才 recall_search，索引信息已足够时不要机械检索。",
		)
	}

	if err := ctx.Err(); err != nil {
		return nil, toolErrorDetails("CONTEXT_DEADLINE_EXCEEDED", err.Error(), "runtime", map[string]any{"budget_ms": r.cfg.ContextBudget().Milliseconds()})
	}
	serializeStarted := time.Now()
	var result Result
	if err := remarshal(contextResult, &result); err != nil {
		return nil, err
	}
	if !nexusLocalOnly {
		result["context_diagnostics"] = map[string]any{
			"complete": instructionSnapshotComplete(instructions) && skillErr == nil && dynamicMCPErr == nil,
			"rules_ms": float64(ruleElapsed) / float64(time.Millisecond), "plugins_ms": float64(pluginElapsed) / float64(time.Millisecond),
			"skills_ms": float64(skillElapsed) / float64(time.Millisecond), "mcp_catalog_ms": float64(mcpElapsed) / float64(time.Millisecond),
			"serialization_ms": float64(time.Since(serializeStarted)) / float64(time.Millisecond), "context_ms": float64(time.Since(started)) / float64(time.Millisecond),
			"plugin_snapshot": pluginInfo, "skill_snapshot": skillInfo, "plugin_build": directory.Metrics,
			"plugin_revision":  directory.Revision,
			"common_skills_ms": float64(commonElapsed) / float64(time.Millisecond), "common_snapshot": commonInfo,
			"task_index_ms": float64(taskElapsed) / float64(time.Millisecond),
		}
	}
	return result, nil
}

func (r *Runtime) agentDockContextTool(ctx context.Context, args map[string]any) (Result, error) {
	var request contextRequest
	if err := decodeToolInput("agentdock_context", args, &request); err != nil {
		return nil, err
	}
	return r.agentDockContext(ctx, false, request.Workdir)
}

type capabilityContext struct {
	Workspace         *workspace.Record           `json:"workspace,omitempty"`
	Tasks             *taskstate.TaskIndex        `json:"tasks,omitempty"`
	InstructionFiles  *agentinstructions.Snapshot `json:"instruction_files,omitempty"`
	Runtime           *capabilityRuntimeContext   `json:"runtime,omitempty"`
	Skills            []capabilitySkillItem       `json:"skills"`
	CommonSkills      *capabilityCommonSkillIndex `json:"common_skills,omitempty"`
	Plugins           []capabilityPluginItem      `json:"plugins,omitempty"`
	DynamicMCP        []capabilityDynamicMCPItem  `json:"dynamic_mcp"`
	ACP               *capabilityACPContext       `json:"acp,omitempty"`
	WorkflowTemplates []capabilityTemplateItem    `json:"workflow_templates"`
	Recall            *capabilityRecallContext    `json:"recall,omitempty"`
	Rules             []string                    `json:"rules"`
	Warnings          []capabilityWarning         `json:"warnings,omitempty"`
}

type capabilityRuntimeContext struct {
	Version             string `json:"version"`
	OS                  string `json:"os"`
	Arch                string `json:"arch"`
	AgentDockHome       string `json:"agentdock_home"`
	AgentDockDefaultDir string `json:"agentdock_default_dir"`
	DefaultCWD          string `json:"default_cwd"`
	PathModel           string `json:"path_model"`
}

type capabilitySkillItem struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	File          string `json:"file"`
	SkillRef      string `json:"skill_ref"`
	SourceType    string `json:"source_type"`
	SourceID      string `json:"source_id"`
	PluginName    string `json:"plugin_name,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
}

type capabilityCommonSkillIndex struct {
	Root      string                      `json:"root"`
	Total     int                         `json:"total"`
	Truncated bool                        `json:"truncated"`
	Items     []capabilityCommonSkillItem `json:"items"`
}

type capabilityCommonSkillItem struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	File          string `json:"file"`
	SkillRef      string `json:"skill_ref"`
	SourceType    string `json:"source_type"`
	SourceID      string `json:"source_id"`
	ContentDigest string `json:"content_digest,omitempty"`
}

type capabilityDynamicMCPItem struct {
	SourceType     string `json:"source_type,omitempty"`
	PluginName     string `json:"plugin_name,omitempty"`
	Revision       string `json:"revision,omitempty"`
	ServerVersion  string `json:"server_version,omitempty"`
	ToolCountKnown *bool  `json:"tool_count_known,omitempty"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	ToolCount      int    `json:"tool_count"`
	LastErrorCode  string `json:"last_error_code,omitempty"`
}

type capabilityPluginItem struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	SkillCount     int    `json:"skill_count"`
	MCPServerCount int    `json:"mcp_server_count"`
}

type capabilityACPContext struct {
	Enabled        bool                          `json:"enabled"`
	DefaultProfile string                        `json:"default_profile"`
	Profiles       []capabilityACPProfileContext `json:"profiles"`
	Description    string                        `json:"description"`
}

type capabilityACPProfileContext struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type capabilityTemplateItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type capabilityMemoryItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type capabilityRecallContext struct {
	Enabled bool                   `json:"enabled"`
	Items   []capabilityMemoryItem `json:"items"`
}

type capabilityWarning struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

type capabilityTemplateList struct {
	Templates []capabilityTemplateListItem `json:"templates"`
}

type capabilityTemplateListItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type capabilityRecallContextIndexResponse struct {
	ContextIndex capabilityRecallContextIndex `json:"context_index"`
}

type capabilityRecallContextIndex struct {
	Items     []capabilityRecallIndexItem `json:"items"`
	Truncated bool                        `json:"truncated"`
}

type capabilityRecallIndexItem struct {
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	Aliases  []string `json:"aliases"`
	Tags     []string `json:"tags"`
	CardType string   `json:"card_type"`
}

func (r *Runtime) dynamicMCPCapabilityIndex(includePluginMembers bool) ([]capabilityDynamicMCPItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.ContextBudget())
	defer cancel()
	directory, _, err := r.pluginStore.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return r.dynamicMCPCapabilityIndexContext(ctx, includePluginMembers, directory)
}
func (r *Runtime) dynamicMCPCapabilityIndexContext(ctx context.Context, includePluginMembers bool, directory *pluginregistry.Directory) ([]capabilityDynamicMCPItem, error) {
	servers, err := r.dynamicMCP.CapabilityItems(ctx, directory)
	if err != nil {
		return []capabilityDynamicMCPItem{}, err
	}
	items := make([]capabilityDynamicMCPItem, 0, len(servers))
	for _, server := range servers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !includePluginMembers {
			membership, owned := directory.MCPMembership(server.Name)
			if owned && membership.Heavy {
				continue
			}
		}
		var known *bool
		revision, version := "", ""
		if !includePluginMembers {
			value := server.ToolCountKnown
			known = &value
			revision = server.Revision
			version = server.ServerVersion
		}
		items = append(items, capabilityDynamicMCPItem{
			Name:     server.Name,
			Revision: revision, ServerVersion: version, ToolCountKnown: known,
			Description:   truncateString(strings.TrimSpace(server.Description), 160),
			SourceType:    capabilitySourceType(server.Plugin),
			PluginName:    server.Plugin,
			Status:        server.Status,
			ToolCount:     server.ToolCount,
			LastErrorCode: server.LastErrorCode,
		})
	}
	return items, nil
}

func (r *Runtime) skillCapabilityIndex(includePluginMembers bool) ([]capabilitySkillItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.ContextBudget())
	defer cancel()
	directory, _, err := r.pluginStore.Snapshot(ctx)
	if err != nil {
		return []capabilitySkillItem{}, err
	}
	items, _, err := r.contextSkillIndex(ctx, directory, includePluginMembers)
	return items, err
}

func (r *Runtime) pluginCapabilityIndex() ([]capabilityPluginItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.ContextBudget())
	defer cancel()
	directory, _, err := r.pluginStore.Snapshot(ctx)
	if err != nil {
		return []capabilityPluginItem{}, err
	}
	return pluginCapabilityIndexFromDirectory(directory), nil
}

func pluginCapabilityIndexFromDirectory(directory *pluginregistry.Directory) []capabilityPluginItem {
	definitions := directory.Definitions()
	items := make([]capabilityPluginItem, 0, len(definitions))
	for _, definition := range definitions {
		if !definition.Enabled || !definition.Heavy {
			continue
		}
		items = append(items, capabilityPluginItem{
			Name: definition.Name, Description: truncateString(strings.TrimSpace(definition.Description), 240),
			SkillCount: len(definition.Skills), MCPServerCount: len(definition.MCPServers),
		})
	}
	return items
}

func (r *Runtime) templateCapabilityIndex(ctx context.Context) ([]capabilityTemplateItem, error) {
	result, err := r.taskTools.WorkflowManage(ctx, tooltask.WorkflowRequest{Action: "list", TemplateStatus: "active"})
	if err != nil {
		return []capabilityTemplateItem{}, err
	}
	var listed capabilityTemplateList
	if err := remarshal(result, &listed); err != nil {
		return []capabilityTemplateItem{}, err
	}
	items := make([]capabilityTemplateItem, 0, len(listed.Templates))
	for _, listedItem := range listed.Templates {
		name := strings.TrimSpace(listedItem.ID)
		if name == "" {
			continue
		}
		items = append(items, capabilityTemplateItem{Name: name, Description: truncateString(strings.TrimSpace(listedItem.Title), 160)})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (r *Runtime) memoryCapabilityIndex(ctx context.Context) ([]capabilityMemoryItem, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(capMaxInt(1000, capMinInt(config.RecallTimeoutMS, 5000)))*time.Millisecond)
	defer cancel()
	result, err := r.recall.ContextIndex(ctx, 3000)
	if err != nil {
		return []capabilityMemoryItem{}, err
	}
	var response capabilityRecallContextIndexResponse
	if err := remarshal(result, &response); err != nil {
		return []capabilityMemoryItem{}, err
	}
	items := make([]capabilityMemoryItem, 0, len(response.ContextIndex.Items))
	seen := make(map[string]struct{}, len(response.ContextIndex.Items))
	for _, item := range response.ContextIndex.Items {
		path := strings.TrimSpace(item.Path)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		items = append(items, capabilityMemoryItem{Name: path, Description: recallIndexDescription(item)})
	}
	return items, nil
}

func recallIndexDescription(item capabilityRecallIndexItem) string {
	if summary := strings.TrimSpace(item.Summary); summary != "" {
		if title := strings.TrimSpace(item.Title); title != "" {
			return truncateString(title+" — "+summary, 360)
		}
		return truncateString(summary, 360)
	}
	parts := []string{}
	if title := strings.TrimSpace(item.Title); title != "" {
		parts = append(parts, title)
	}
	if kind := strings.TrimSpace(item.Kind); kind != "" {
		parts = append(parts, kind)
	}
	if cardType := strings.TrimSpace(item.CardType); cardType != "" {
		parts = append(parts, cardType)
	}
	labels := append(append(append([]string{}, item.Keywords...), item.Aliases...), item.Tags...)
	if len(labels) > 0 {
		parts = append(parts, strings.Join(labels, ", "))
	}
	return truncateString(strings.Join(parts, " · "), 360)
}

func capMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func capMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func capabilitySourceType(plugin string) string {
	if plugin != "" {
		return "plugin"
	}
	return "standalone"
}

func instructionSnapshotComplete(value agentinstructions.Snapshot) bool {
	if !value.AutoLoad {
		return true
	}
	for _, file := range value.Files {
		if file.Status == "error" || file.Status == "skipped" {
			return false
		}
	}
	return true
}
