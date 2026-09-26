package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type repeatedString []string

func (values *repeatedString) String() string { return strings.Join(*values, ",") }
func (values *repeatedString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("值不能为空")
	}
	*values = append(*values, value)
	return nil
}

func runControlCommand(ctx context.Context, command string, args []string, stdout, stderr io.Writer) error {
	switch command {
	case "status":
		return runStatusControl(ctx, args, stdout, stderr)
	case "task":
		return runTaskControl(ctx, args, stdout, stderr)
	case "activity":
		return runActivityControl(ctx, args, stdout, stderr)
	case "workspace":
		return runWorkspaceControl(ctx, args, stdout, stderr)
	case "skill":
		return runSkillControl(ctx, args, stdout, stderr)
	case "plugin":
		return runPluginControl(ctx, args, stdout, stderr)
	case "conversation":
		return runConversationControl(ctx, args, stdout, stderr)
	case "call":
		return runCallControl(ctx, args, stdout, stderr)
	case "approval":
		return runApprovalControl(ctx, args, stdout, stderr)
	case "permission":
		return runPermissionControl(ctx, args, stdout, stderr)
	case "insertion":
		return runInsertionControl(ctx, args, stdout, stderr)
	case "doctor":
		return runDoctorControl(ctx, args, stdout, stderr)
	default:
		return controlErrorf(controlExitInvalid, "未知控制命令 %q", command)
	}
}

type controlFlagSet struct {
	*flag.FlagSet
	positionals []string
}

func newControlFlagSet(name string, stderr io.Writer, options *controlOptions) *controlFlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	addControlFlags(flags, options)
	return &controlFlagSet{FlagSet: flags}
}

// Parse keeps the standard flag package and its validation while allowing the
// conventional `command <id> --json` form. The standard package otherwise
// stops at the first positional argument, which made trailing control options
// look like extra identifiers.
func (flags *controlFlagSet) Parse(args []string) error {
	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if argument == "-" || !strings.HasPrefix(argument, "-") {
			positionals = append(positionals, argument)
			continue
		}
		nameValue := strings.TrimLeft(argument, "-")
		name, _, hasValue := strings.Cut(nameValue, "=")
		definition := flags.Lookup(name)
		flagArgs = append(flagArgs, argument)
		if definition == nil || hasValue {
			continue
		}
		if boolean, ok := definition.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if index+1 >= len(args) {
			return fmt.Errorf("flag needs an argument: --%s", name)
		}
		index++
		flagArgs = append(flagArgs, args[index])
	}
	flags.positionals = positionals
	return flags.FlagSet.Parse(flagArgs)
}

func (flags *controlFlagSet) Args() []string {
	return append(append([]string{}, flags.positionals...), flags.FlagSet.Args()...)
}

func (flags *controlFlagSet) NArg() int { return len(flags.Args()) }

func runStatusControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var raw controlOptions
	flags := newControlFlagSet("agentdock status", stderr, &raw)
	if err := flags.Parse(args); err != nil {
		return controlWrap(controlExitInvalid, "解析 status 参数失败", err)
	}
	if flags.NArg() != 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock status [控制选项]")
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	result, err := newControlClient(options, stderr).request(ctx, http.MethodGet, "/internal/runtime/status", nil, nil)
	if err != nil {
		return err
	}
	return writeControlOutput(stdout, options, result, "")
}

func runTaskControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock task <create|list|show|steps|calls|follow|checkpoint|block|resume|final-review|complete|cancel|pin|unpin|tags|archive|unarchive|trash|restore|rename|delete|export> ...")
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock task "+action, stderr, &raw)
	status := flags.String("status", "", "状态筛选、checkpoint 状态或 final-review 状态")
	view := flags.String("view", "", "任务视图：active、archived、trash 或 all")
	search := flags.String("search", "", "搜索标题、目标或摘要")
	tag := flags.String("tag", "", "单个标签筛选")
	selection := flags.Bool("selection", false, "返回当前筛选的稳定选择集")
	offset := flags.Int("offset", 0, "分页偏移")
	limit := flags.Int("limit", 50, "最大返回数量（1-200）")
	title := flags.String("title", "", "任务标题或重命名后的标题")
	goal := flags.String("goal", "", "任务目标")
	device := flags.String("device", "", "设备标识")
	stepID := flags.String("step", "", "单步 checkpoint 的 step_id")
	currentStep := flags.String("current-step", "", "批量 checkpoint 后的当前 step_id")
	summary := flags.String("summary", "", "进度、阻塞、恢复、取消或终审摘要")
	retentionDays := flags.Int("retention-days", 30, "移入回收站后的保留天数（1-3650）")
	confirmPermanent := flags.Bool("confirm-permanent", false, "确认永久删除任务管理对象；项目文件不会删除")
	outputFile := flags.String("output", "", "export 输出文件；默认 stdout")
	includeOutput := flags.Bool("include-output", false, "调用列表包含已保存输出")
	after := flags.Uint64("after", 0, "调用序号游标")
	before := flags.Uint64("before", 0, "调用反向序号游标")
	var conditions repeatedString
	var steps repeatedString
	var completedSteps repeatedString
	var tags repeatedString
	var verified repeatedString
	var risks repeatedString
	flags.Var(&conditions, "condition", "完成条件；可重复")
	flags.Var(&steps, "task-step", "任务步骤 id=title；可重复")
	flags.Var(&completedSteps, "completed-step", "批量完成的 step_id；可重复")
	flags.Var(&tags, "set-tag", "替换后的标签；可重复")
	flags.Var(&verified, "verified", "终审已验证事实；可重复")
	flags.Var(&risks, "risk", "终审未解决风险；可重复")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 task 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	positionals := flags.Args()

	if action == "list" {
		if len(positionals) != 0 || *limit < 1 || *limit > 200 || *offset < 0 || *offset > 20000 {
			return controlErrorf(controlExitInvalid, "task list 需要 --limit 1..200、--offset 0..20000，且不接受位置参数")
		}
		query := url.Values{"limit": {fmt.Sprint(*limit)}, "offset": {fmt.Sprint(*offset)}}
		for key, value := range map[string]string{
			"status": *status, "view": *view, "search": *search, "tag": *tag, "workspace_id": options.workspaceID,
		} {
			if value = strings.TrimSpace(value); value != "" {
				query.Set(key, value)
			}
		}
		if *selection {
			query.Set("selection", "true")
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/execution/tasks", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "tasks")
	}
	if action == "create" {
		if len(positionals) != 0 || strings.TrimSpace(*title) == "" || strings.TrimSpace(*goal) == "" || len(conditions) == 0 {
			return controlErrorf(controlExitInvalid, "用法：agentdock task create --title <标题> --goal <目标> --condition <条件> [--task-step id=title]")
		}
		parsedSteps, parseErr := parseTaskSteps(steps)
		if parseErr != nil {
			return parseErr
		}
		body := map[string]any{
			"action": "create", "title": strings.TrimSpace(*title), "goal": strings.TrimSpace(*goal),
			"completion_conditions": []string(conditions),
		}
		if len(parsedSteps) > 0 {
			body["steps"] = parsedSteps
		}
		if options.workspaceID != "" {
			body["workspace_id"] = options.workspaceID
		}
		if options.project != "" {
			body["project"] = options.project
		}
		if value := strings.TrimSpace(*device); value != "" {
			body["device"] = value
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/tasks", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	}

	id, parseErr := oneIdentifier(positionals, options.taskID, "task_id")
	if parseErr != nil {
		return parseErr
	}
	base := "/internal/runtime/tasks/" + pathSegment(id)
	switch action {
	case "show":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "steps":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		stepsValue, ok := findAnyField(result, "steps")
		if !ok {
			return controlErrorf(controlExitService, "任务详情未返回 steps")
		}
		return writeControlOutput(stdout, options, map[string]any{"task_id": id, "steps": stepsValue}, "steps")
	case "calls":
		if *limit < 1 || *limit > 200 {
			return controlErrorf(controlExitInvalid, "task calls 的 --limit 必须在 1..200")
		}
		query := url.Values{"limit": {fmt.Sprint(*limit)}}
		if *after > 0 {
			query.Set("after", fmt.Sprint(*after))
		}
		if *before > 0 {
			query.Set("before", fmt.Sprint(*before))
		}
		if *includeOutput {
			query.Set("include_output", "true")
		}
		result, requestErr := client.request(ctx, http.MethodGet, base+"/calls", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "calls")
	case "follow", "watch":
		if *limit < 1 || *limit > 200 {
			return controlErrorf(controlExitInvalid, "task follow 的 --limit 必须在 1..200")
		}
		query := url.Values{"task_id": {id}, "limit": {fmt.Sprint(*limit)}}
		if *after > 0 {
			query.Set("after", fmt.Sprint(*after))
		}
		if *before > 0 {
			query.Set("before", fmt.Sprint(*before))
		}
		if *includeOutput {
			query.Set("include_output", "true")
		}
		return client.watchSSE(ctx, "/internal/runtime/calls/stream", query, stdout, options)
	case "export":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlExport(stdout, options, *outputFile, result, "")
	case "checkpoint":
		if strings.TrimSpace(*summary) == "" {
			return controlErrorf(controlExitInvalid, "task checkpoint 需要 --summary")
		}
		single := strings.TrimSpace(*stepID) != "" || strings.TrimSpace(*status) != ""
		batch := len(completedSteps) > 0 || strings.TrimSpace(*currentStep) != ""
		if single == batch {
			return controlErrorf(controlExitInvalid, "checkpoint 必须且只能选择单步 (--step/--status) 或批量 (--completed-step/--current-step) 模式")
		}
		body := map[string]any{"action": "checkpoint", "task_id": id, "summary": strings.TrimSpace(*summary)}
		if single {
			if strings.TrimSpace(*stepID) == "" || (*status != "completed" && *status != "in_progress") {
				return controlErrorf(controlExitInvalid, "单步 checkpoint 需要 --step 和 --status completed|in_progress")
			}
			body["step_id"] = strings.TrimSpace(*stepID)
			body["status"] = strings.TrimSpace(*status)
		} else {
			body["completed_step_ids"] = []string(completedSteps)
			if value := strings.TrimSpace(*currentStep); value != "" {
				body["current_step_id"] = value
			}
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/tasks", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "block", "resume", "cancel":
		if strings.TrimSpace(*summary) == "" {
			return controlErrorf(controlExitInvalid, "task %s 需要 --summary", action)
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/tasks", nil, map[string]any{"action": action, "task_id": id, "summary": strings.TrimSpace(*summary)})
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "final-review", "final_review":
		reviewStatus := strings.ToLower(strings.TrimSpace(*status))
		if reviewStatus != "pass" && reviewStatus != "failed" {
			return controlErrorf(controlExitInvalid, "task final-review 需要 --status pass|failed")
		}
		if strings.TrimSpace(*summary) == "" {
			return controlErrorf(controlExitInvalid, "task final-review 需要 --summary")
		}
		if reviewStatus == "pass" && len(verified) == 0 {
			return controlErrorf(controlExitInvalid, "通过终审至少需要一个 --verified")
		}
		if reviewStatus == "failed" && len(risks) == 0 {
			return controlErrorf(controlExitInvalid, "失败终审至少需要一个 --risk")
		}
		body := map[string]any{"action": "final_review", "task_id": id, "status": reviewStatus, "summary": strings.TrimSpace(*summary), "verified": []string(verified), "risks": []string(risks)}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/tasks", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "complete":
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/tasks", nil, map[string]any{"action": "complete", "task_id": id})
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "pin", "unpin", "archive", "unarchive", "trash", "restore", "delete", "purge", "rename", "tags":
		batchAction := action
		if batchAction == "purge" {
			batchAction = "delete"
		}
		if batchAction == "rename" && strings.TrimSpace(*title) == "" {
			return controlErrorf(controlExitInvalid, "task rename 需要 --title")
		}
		if batchAction == "tags" && len(tags) == 0 {
			return controlErrorf(controlExitInvalid, "task tags 至少需要一个 --set-tag；清空标签请使用 --set-tag 并明确空集合当前不受支持")
		}
		if batchAction == "trash" && (*retentionDays < 1 || *retentionDays > 3650) {
			return controlErrorf(controlExitInvalid, "retention-days 必须在 1..3650")
		}
		if batchAction == "delete" && !*confirmPermanent {
			return controlErrorf(controlExitInvalid, "永久删除任务管理对象需要 --confirm-permanent；项目文件不会删除")
		}
		body := map[string]any{"ids": []string{id}, "action": batchAction}
		if batchAction == "rename" {
			body["title"] = strings.TrimSpace(*title)
		}
		if batchAction == "tags" {
			body["tags"] = []string(tags)
		}
		if batchAction == "trash" {
			body["retention_days"] = *retentionDays
		}
		if batchAction == "delete" {
			body["confirm_permanent"] = true
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/tasks/batch", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlBatchResult(stdout, options, result)
	default:
		return controlErrorf(controlExitInvalid, "未知 task 子命令 %q", action)
	}
}

func runActivityControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock activity <list|watch|export> ...")
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock activity "+action, stderr, &raw)
	limit := flags.Int("limit", 100, "最大返回数量")
	after := flags.Int64("after", 0, "从指定序号之后读取")
	before := flags.Int64("before", 0, "读取指定序号之前的记录")
	outputFile := flags.String("output", "", "export 输出文件；默认 stdout")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 activity 参数失败", err)
	}
	if flags.NArg() != 0 || *limit < 1 || *limit > 500 || *after < 0 || *before < 0 {
		return controlErrorf(controlExitInvalid, "activity 参数无效")
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	query := selectorQuery(options)
	query.Set("limit", fmt.Sprint(*limit))
	setPositiveQuery(query, "after", *after)
	setPositiveQuery(query, "before", *before)
	client := newControlClient(options, stderr)
	switch action {
	case "list":
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/activity", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "events")
	case "watch":
		if *outputFile != "" {
			return controlErrorf(controlExitInvalid, "activity watch 不接受 --output")
		}
		return client.watchSSE(ctx, "/internal/runtime/activity/stream", query, stdout, options)
	case "export":
		if *limit > 200 {
			return controlErrorf(controlExitInvalid, "activity export 的 --limit 不能超过 200")
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/calls/export", query, nil)
		if requestErr != nil {
			return requestErr
		}
		if strings.TrimSpace(*outputFile) == "" {
			return writeControlOutput(stdout, options, result, "calls")
		}
		if options.quiet {
			return controlErrorf(controlExitInvalid, "activity export --output 不能与 --quiet 一起使用")
		}
		data, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return controlWrap(controlExitService, "编码导出失败", marshalErr)
		}
		data = append(data, '\n')
		if writeErr := os.WriteFile(*outputFile, data, 0600); writeErr != nil {
			return controlWrap(controlExitService, "写入导出文件失败", writeErr)
		}
		return writeControlOutput(stdout, options, map[string]any{"ok": true, "output": *outputFile, "bytes": len(data)}, "")
	default:
		return controlErrorf(controlExitInvalid, "未知 activity 子命令 %q", action)
	}
}

func runWorkspaceControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock workspace <list|show|status|create|register|update|validate|resolve> ...")
	}
	action := strings.ToLower(args[0])
	if action == "register" {
		action = "create"
	}
	var raw controlOptions
	flags := newControlFlagSet("agentdock workspace "+action, stderr, &raw)
	name := flags.String("name", "", "工作区显示名称")
	kind := flags.String("kind", "", "工作区类型：repository 或 directory")
	runtimeName := flags.String("runtime", "", "运行时：windows、unix 或 wsl")
	distribution := flags.String("wsl-distribution", "", "WSL 发行版")
	root := flags.String("root", "", "显式绝对项目根目录")
	defaultWorkdir := flags.String("default-workdir", "", "根目录内的默认相对工作目录")
	artifactRoot := flags.String("artifact-root", "", "显式绝对产物根目录")
	scratchRoot := flags.String("scratch-root", "", "显式绝对临时目录")
	cacheRoot := flags.String("cache-root", "", "显式绝对缓存目录")
	createRoot := flags.Bool("create-root", false, "创建显式指定的原生项目根目录")
	expectedRevision := flags.Int("expected-revision", 0, "更新时必需的当前 rules_revision")
	targetKind := flags.String("target-kind", "source", "解析目标：source、artifact、scratch、cache 或 external")
	logicalPath := flags.String("path", ".", "目标类型内的逻辑相对路径")
	externalPath := flags.String("external-path", "", "单次外部绝对路径；不会保存为工作区默认值")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 workspace 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	positionals := flags.Args()
	switch action {
	case "list":
		if len(positionals) != 0 {
			return controlErrorf(controlExitInvalid, "workspace list 不接受位置参数")
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/workspaces", nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "workspaces")
	case "show", "status":
		id, parseErr := oneIdentifier(positionals, options.workspaceID, "workspace_id")
		if parseErr != nil {
			return parseErr
		}
		query := url.Values{}
		if options.project != "" {
			query.Set("project", options.project)
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/workspaces/"+pathSegment(id), query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "create", "update":
		id := ""
		if action == "update" {
			var parseErr error
			id, parseErr = oneIdentifier(positionals, options.workspaceID, "workspace_id")
			if parseErr != nil {
				return parseErr
			}
			if *expectedRevision < 1 {
				return controlErrorf(controlExitInvalid, "workspace update 需要 --expected-revision，以防覆盖并发修改")
			}
		} else if len(positionals) != 0 {
			return controlErrorf(controlExitInvalid, "workspace create 不接受位置参数")
		}
		if action == "create" && strings.TrimSpace(*root) == "" {
			return controlErrorf(controlExitInvalid, "workspace create 需要显式 --root")
		}
		body := map[string]any{"action": "register"}
		if id != "" {
			body["workspace_id"] = id
			body["expected_revision"] = *expectedRevision
		}
		for key, value := range map[string]string{
			"name": *name, "kind": *kind, "project": options.project,
			"runtime": *runtimeName, "wsl_distribution": *distribution,
			"root": *root, "default_workdir": *defaultWorkdir,
			"artifact_root": *artifactRoot, "scratch_root": *scratchRoot, "cache_root": *cacheRoot,
		} {
			if value = strings.TrimSpace(value); value != "" {
				body[key] = value
			}
		}
		if *createRoot {
			body["create_root"] = true
		}
		if action == "update" && len(body) == 3 {
			return controlErrorf(controlExitInvalid, "workspace update 至少需要一个要修改的字段")
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/workspaces", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "validate", "resolve":
		id, parseErr := oneIdentifier(positionals, options.workspaceID, "workspace_id")
		if parseErr != nil {
			return parseErr
		}
		kindValue := strings.ToLower(strings.TrimSpace(*targetKind))
		if kindValue != "source" && kindValue != "artifact" && kindValue != "scratch" && kindValue != "cache" && kindValue != "external" {
			return controlErrorf(controlExitInvalid, "workspace resolve 的 --target-kind 无效")
		}
		body := map[string]any{"action": "resolve", "workspace_id": id, "target_kind": kindValue}
		if options.project != "" {
			body["project"] = options.project
		}
		if value := strings.TrimSpace(*logicalPath); value != "" {
			body["path"] = value
		}
		if options.taskID != "" {
			body["task_id"] = options.taskID
		}
		if value := strings.TrimSpace(*externalPath); value != "" {
			body["external_path"] = value
		}
		if kindValue == "external" && strings.TrimSpace(*externalPath) == "" {
			return controlErrorf(controlExitInvalid, "external 目标需要 --external-path")
		}
		if (kindValue == "artifact" || kindValue == "scratch") && options.taskID == "" {
			return controlErrorf(controlExitInvalid, "%s 目标需要 --task", kindValue)
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/workspaces", nil, body)
		if requestErr != nil {
			return requestErr
		}
		if action == "validate" {
			result = map[string]any{"ok": true, "validated": true, "resolution": result}
		}
		return writeControlOutput(stdout, options, result, "")
	default:
		return controlErrorf(controlExitInvalid, "未知 workspace 子命令 %q", action)
	}
}

func runSkillControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runNamedReadControl(ctx, "skill", "skills", args, stdout, stderr)
}

func runPluginControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runNamedReadControl(ctx, "plugin", "plugins", args, stdout, stderr)
}

func runNamedReadControl(ctx context.Context, noun, route string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock %s <list|show|status%s> ...", noun, map[bool]string{true: "|test"}[noun == "plugin"])
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock "+noun+" "+action, stderr, &raw)
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 "+noun+" 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	positionals := flags.Args()
	switch action {
	case "list":
		if len(positionals) != 0 {
			return controlErrorf(controlExitInvalid, "%s list 不接受位置参数", noun)
		}
		query := url.Values{}
		if noun == "skill" {
			query.Set("summary", "true")
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/"+route, query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, route)
	case "test":
		if noun != "plugin" {
			return controlErrorf(controlExitUnsupported, "skill 不支持 test")
		}
		return &controlError{code: controlExitUnsupported, stableCode: "PLUGIN_TEST_UNSUPPORTED", message: "Core 当前没有插件主动测试接口；未执行测试"}
	case "show", "status":
		id, parseErr := oneIdentifier(positionals, "", noun+" name")
		if parseErr != nil {
			return parseErr
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/"+route+"/"+pathSegment(id), nil, nil)
		if requestErr != nil {
			return requestErr
		}
		if action == "status" {
			result = map[string]any{"ok": true, "reachable": true, noun: result}
		}
		return writeControlOutput(stdout, options, result, "")
	default:
		return controlErrorf(controlExitInvalid, "未知 %s 子命令 %q", noun, action)
	}
}

func selectorQuery(options resolvedControlOptions) url.Values {
	query := url.Values{}
	for key, value := range map[string]string{
		"workspace_id":    options.workspaceID,
		"project":         options.project,
		"task_id":         options.taskID,
		"thread_id":       options.threadID,
		"conversation_id": options.conversation,
	} {
		if value != "" {
			query.Set(key, value)
		}
	}
	return query
}

func oneIdentifier(positionals []string, fallback, label string) (string, error) {
	if len(positionals) > 1 {
		return "", controlErrorf(controlExitInvalid, "%s 只能提供一个位置参数", label)
	}
	value := strings.TrimSpace(fallback)
	if len(positionals) == 1 {
		if value != "" && value != strings.TrimSpace(positionals[0]) {
			return "", controlErrorf(controlExitInvalid, "%s 同时由位置参数和全局选项指定且不一致", label)
		}
		value = strings.TrimSpace(positionals[0])
	}
	if value == "" {
		return "", controlErrorf(controlExitInvalid, "缺少 %s", label)
	}
	return value, nil
}

func parseTaskSteps(values []string) ([]map[string]string, error) {
	steps := make([]map[string]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		id, title, ok := strings.Cut(value, "=")
		id, title = strings.TrimSpace(id), strings.TrimSpace(title)
		if !ok || id == "" || title == "" {
			return nil, controlErrorf(controlExitInvalid, "task-step 必须使用 id=title: %q", value)
		}
		if seen[id] {
			return nil, controlErrorf(controlExitInvalid, "task-step id 重复: %s", id)
		}
		seen[id] = true
		steps = append(steps, map[string]string{"id": id, "title": title})
	}
	return steps, nil
}
