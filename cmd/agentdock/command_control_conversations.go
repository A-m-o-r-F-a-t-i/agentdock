package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func runConversationControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock conversation <list|show|tasks|calls|follow|attach|detach|link|stop|resume|pin|unpin|tags|archive|unarchive|trash|restore|rename|delete|export> ...")
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock conversation "+action, stderr, &raw)
	limit := flags.Int("limit", 100, "最大返回数量（1-200）")
	offset := flags.Int("offset", 0, "分页偏移")
	search := flags.String("search", "", "搜索文本")
	tag := flags.String("tag", "", "标签筛选")
	view := flags.String("view", "", "视图筛选")
	selection := flags.Bool("selection", false, "返回可选择项")
	taskThread := flags.String("task-thread", "", "要绑定的任务线程")
	title := flags.String("title", "", "重命名后的会话标题")
	retentionDays := flags.Int("retention-days", 30, "回收站保留天数（1-3650）")
	confirmPermanent := flags.Bool("confirm-permanent", false, "确认永久删除会话管理对象；项目文件不会删除")
	outputFile := flags.String("output", "", "export 输出文件；默认 stdout")
	includeCalls := flags.Bool("include-calls", false, "export 同时包含最多 limit 条调用")
	includeOutput := flags.Bool("include-output", false, "调用查询包含已保存输出")
	after := flags.Uint64("after", 0, "调用序号游标")
	before := flags.Uint64("before", 0, "调用反向序号游标")
	var tags repeatedString
	flags.Var(&tags, "set-tag", "替换后的标签；可重复")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 conversation 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	positionals := flags.Args()
	if action == "list" {
		if len(positionals) != 0 || *limit < 1 || *limit > 200 || *offset < 0 || *offset > 20000 {
			return controlErrorf(controlExitInvalid, "conversation list 分页参数无效")
		}
		query := selectorQuery(options)
		query.Set("limit", strconv.Itoa(*limit))
		query.Set("offset", strconv.Itoa(*offset))
		if *selection {
			query.Set("selection", "true")
		}
		for key, value := range map[string]string{"search": *search, "tag": *tag, "view": *view} {
			if value = strings.TrimSpace(value); value != "" {
				query.Set(key, value)
			}
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/conversations", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "conversations")
	}
	id, parseErr := oneIdentifier(positionals, options.conversation, "conversation_id")
	if parseErr != nil {
		return parseErr
	}
	base := "/internal/runtime/conversations/" + pathSegment(id)
	callQuery := func() (url.Values, error) {
		if *limit < 1 || *limit > 200 {
			return nil, controlErrorf(controlExitInvalid, "conversation 调用查询的 --limit 必须在 1..200")
		}
		query := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *after > 0 {
			query.Set("after", strconv.FormatUint(*after, 10))
		}
		if *before > 0 {
			query.Set("before", strconv.FormatUint(*before, 10))
		}
		if *includeOutput {
			query.Set("include_output", "true")
		}
		return query, nil
	}
	switch action {
	case "show":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "tasks":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		taskIDs, ok := findAnyField(result, "task_ids")
		if !ok {
			return controlErrorf(controlExitService, "会话详情未返回 task_ids")
		}
		return writeControlOutput(stdout, options, map[string]any{"conversation_id": id, "task_ids": taskIDs}, "task_ids")
	case "calls":
		query, queryErr := callQuery()
		if queryErr != nil {
			return queryErr
		}
		result, requestErr := client.request(ctx, http.MethodGet, base+"/calls", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "calls")
	case "follow", "watch":
		query, queryErr := callQuery()
		if queryErr != nil {
			return queryErr
		}
		return client.watchSSE(ctx, base+"/stream", query, stdout, options)
	case "export":
		detail, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		result := map[string]any{"conversation": detail}
		if *includeCalls {
			query, queryErr := callQuery()
			if queryErr != nil {
				return queryErr
			}
			calls, callsErr := client.request(ctx, http.MethodGet, base+"/calls", query, nil)
			if callsErr != nil {
				return callsErr
			}
			result["calls"] = calls
		}
		return writeControlExport(stdout, options, *outputFile, result, "")
	case "attach", "detach":
		detail, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		revision, ok := findUintField(detail, "binding_revision")
		if !ok || revision == 0 {
			return controlErrorf(controlExitConflict, "会话详情未返回 binding_revision，无法安全更新绑定")
		}
		body := map[string]any{"task_id": "", "binding_revision": revision}
		if action == "attach" {
			if options.taskID == "" {
				return controlErrorf(controlExitInvalid, "conversation attach 需要 --task <task_id>")
			}
			body["task_id"] = options.taskID
			if value := strings.TrimSpace(*taskThread); value != "" {
				body["task_thread_id"] = value
			}
		}
		result, requestErr := client.request(ctx, http.MethodPost, base+"/current-task", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "link":
		if options.taskID == "" {
			return controlErrorf(controlExitInvalid, "conversation link 需要 --task <task_id>")
		}
		result, requestErr := client.request(ctx, http.MethodPost, base+"/link-task", nil, map[string]any{"task_id": options.taskID})
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "stop", "terminate", "resume":
		lifecycle := action
		if lifecycle == "stop" {
			lifecycle = "terminate"
		}
		result, requestErr := client.request(ctx, http.MethodPost, base+"/"+lifecycle, nil, map[string]any{"confirm": true})
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
			return controlErrorf(controlExitInvalid, "conversation rename 需要 --title")
		}
		if batchAction == "tags" && len(tags) == 0 {
			return controlErrorf(controlExitInvalid, "conversation tags 至少需要一个 --set-tag")
		}
		if batchAction == "trash" && (*retentionDays < 1 || *retentionDays > 3650) {
			return controlErrorf(controlExitInvalid, "retention-days 必须在 1..3650")
		}
		if batchAction == "delete" && !*confirmPermanent {
			return controlErrorf(controlExitInvalid, "永久删除会话管理对象需要 --confirm-permanent；项目文件不会删除")
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
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/conversations/batch", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlBatchResult(stdout, options, result)
	default:
		return controlErrorf(controlExitInvalid, "未知 conversation 子命令 %q", action)
	}
}
