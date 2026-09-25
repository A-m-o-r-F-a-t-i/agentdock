package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func runCallControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock call <list|show|events|logs|wait|follow|cancel|export|archive|unarchive|isolate|unisolate|trash|restore|delete> ...")
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock call "+action, stderr, &raw)
	limit := flags.Int("limit", 100, "最大返回数量（调用 1-200；事件 1-500）")
	after := flags.Uint64("after", 0, "从序号之后读取")
	before := flags.Uint64("before", 0, "在序号之前读取")
	status := flags.String("status", "", "调用状态筛选")
	view := flags.String("view", "", "调用视图：active、archived、trash、isolated 或 all")
	search := flags.String("search", "", "搜索工具名、标题或摘要")
	parent := flags.String("parent", "", "parent_call_id 筛选")
	unattributed := flags.Bool("unattributed", false, "仅返回未归属调用")
	topLevel := flags.Bool("top-level", false, "仅返回顶层调用")
	includeOutput := flags.Bool("include-output", false, "在列表或导出中包含输出")
	includeDiagnostic := flags.Bool("include-diagnostic", false, "包含诊断调用")
	payloadKind := flags.String("kind", "response", "日志载荷：request、response 或 source")
	offset := flags.Int64("offset", 0, "日志字节偏移")
	limitChars := flags.Int("limit-chars", 100000, "日志 Unicode 标量上限（1-100000）")
	rawPayload := flags.Bool("raw", false, "仅输出日志文本；不能与 JSON/JSONL 同用")
	waitTimeout := flags.Duration("wait-timeout", 5*time.Minute, "等待终态的最长时间")
	pollInterval := flags.Duration("poll-interval", time.Second, "wait 查询间隔")
	outputFile := flags.String("output", "", "export 输出文件；默认 stdout")
	retentionDays := flags.Int("retention-days", 7, "移入回收站后的保留天数")
	confirmPermanent := flags.Bool("confirm-permanent", false, "确认永久删除管理记录")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 call 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	positionals := flags.Args()

	query := url.Values{}
	for key, value := range map[string]string{
		"task_id": options.taskID, "thread_id": options.threadID,
		"conversation_id": options.conversation, "status": strings.TrimSpace(*status),
		"view": strings.TrimSpace(*view), "search": strings.TrimSpace(*search),
		"parent_call_id": strings.TrimSpace(*parent),
	} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if *after > 0 {
		query.Set("after", strconv.FormatUint(*after, 10))
	}
	if *before > 0 {
		query.Set("before", strconv.FormatUint(*before, 10))
	}
	if *unattributed {
		query.Set("unattributed", "true")
	}
	if *topLevel {
		query.Set("top_level", "true")
	}
	if *includeOutput {
		query.Set("include_output", "true")
	}
	if *includeDiagnostic {
		query.Set("include_diagnostic", "true")
	}

	switch action {
	case "list":
		if len(positionals) != 0 || *limit < 1 || *limit > 200 {
			return controlErrorf(controlExitInvalid, "call list 的 --limit 必须在 1..200，且不接受位置参数")
		}
		query.Set("limit", strconv.Itoa(*limit))
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/calls", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "calls")
	case "follow", "watch":
		if len(positionals) != 0 || *limit < 1 || *limit > 200 {
			return controlErrorf(controlExitInvalid, "call follow 的 --limit 必须在 1..200，且不接受位置参数")
		}
		query.Set("limit", strconv.Itoa(*limit))
		return client.watchSSE(ctx, "/internal/runtime/calls/stream", query, stdout, options)
	case "export":
		if len(positionals) != 0 || *limit < 1 || *limit > 200 {
			return controlErrorf(controlExitInvalid, "call export 的 --limit 必须在 1..200，且不接受位置参数")
		}
		query.Set("limit", strconv.Itoa(*limit))
		query.Set("include_output", "true")
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/calls/export", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlExport(stdout, options, *outputFile, result, "calls")
	}

	id, parseErr := oneIdentifier(positionals, options.callID, "call_id")
	if parseErr != nil {
		return parseErr
	}
	base := "/internal/runtime/calls/" + pathSegment(id)
	switch action {
	case "show":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "events":
		if *limit < 1 || *limit > 500 {
			return controlErrorf(controlExitInvalid, "call events 的 --limit 必须在 1..500")
		}
		eventQuery := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *after > 0 {
			eventQuery.Set("after", strconv.FormatUint(*after, 10))
		}
		result, requestErr := client.request(ctx, http.MethodGet, base+"/events", eventQuery, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "events")
	case "logs":
		kind := strings.ToLower(strings.TrimSpace(*payloadKind))
		if kind != "request" && kind != "response" && kind != "source" {
			return controlErrorf(controlExitInvalid, "call logs 的 --kind 必须是 request、response 或 source")
		}
		if *offset < 0 || *limitChars < 1 || *limitChars > 100000 {
			return controlErrorf(controlExitInvalid, "call logs 需要非负 offset 和 1..100000 的 limit-chars")
		}
		if *rawPayload && (options.jsonOutput || options.jsonLines) {
			return controlErrorf(controlExitInvalid, "call logs --raw 不能与 JSON/JSONL 输出同用")
		}
		payloadQuery := url.Values{"offset": {strconv.FormatInt(*offset, 10)}, "limit_chars": {strconv.Itoa(*limitChars)}}
		result, requestErr := client.request(ctx, http.MethodGet, base+"/payload/"+kind, payloadQuery, nil)
		if requestErr != nil {
			return requestErr
		}
		if *rawPayload {
			if options.quiet {
				return nil
			}
			text, ok := directStringField(result, "text")
			if !ok {
				return controlErrorf(controlExitService, "调用载荷响应缺少 text")
			}
			_, writeErr := io.WriteString(stdout, text)
			if writeErr == nil && text != "" && !strings.HasSuffix(text, "\n") {
				_, writeErr = io.WriteString(stdout, "\n")
			}
			if writeErr != nil {
				return controlWrap(controlExitService, "写入调用日志失败", writeErr)
			}
			return nil
		}
		return writeControlOutput(stdout, options, result, "")
	case "wait":
		if *waitTimeout <= 0 || *waitTimeout > 24*time.Hour || *pollInterval < 100*time.Millisecond || *pollInterval > time.Minute {
			return controlErrorf(controlExitInvalid, "call wait 需要大于 0 且不超过 24h 的 wait-timeout，以及 100ms..1m 的 poll-interval")
		}
		waitCtx, cancel := context.WithTimeout(ctx, *waitTimeout)
		defer cancel()
		for {
			result, requestErr := client.request(waitCtx, http.MethodGet, base, nil, nil)
			if requestErr != nil {
				if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
					return &controlError{code: controlExitTimeout, stableCode: "WAIT_TIMEOUT", message: "等待调用终态超时"}
				}
				return requestErr
			}
			statusValue, _ := findStringField(result, "status")
			if callStatusTerminal(statusValue) {
				if outputErr := writeControlOutput(stdout, options, result, ""); outputErr != nil {
					return outputErr
				}
				if strings.EqualFold(statusValue, "succeeded") {
					return nil
				}
				exitCode := controlExitFailure
				if strings.EqualFold(statusValue, "partial") {
					exitCode = controlExitPartial
				}
				return markControlErrorReported(&controlError{
					code:       exitCode,
					stableCode: "CALL_STATUS_" + strings.ToUpper(statusValue),
					message:    "调用以非成功终态结束: " + statusValue,
				})
			}
			timer := time.NewTimer(*pollInterval)
			select {
			case <-waitCtx.Done():
				timer.Stop()
				return &controlError{code: controlExitTimeout, stableCode: "WAIT_TIMEOUT", message: "等待调用终态超时"}
			case <-timer.C:
			}
		}
	case "cancel", "stop":
		result, requestErr := client.request(ctx, http.MethodPost, base+"/stop", nil, map[string]any{})
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "archive", "unarchive", "isolate", "unisolate", "trash", "restore", "delete", "purge":
		batchAction := action
		if batchAction == "purge" {
			batchAction = "delete"
		}
		if batchAction == "delete" && !*confirmPermanent {
			return controlErrorf(controlExitInvalid, "永久删除调用管理记录需要 --confirm-permanent")
		}
		if *retentionDays < 1 || *retentionDays > 365 {
			return controlErrorf(controlExitInvalid, "retention-days 必须在 1..365")
		}
		body := map[string]any{"ids": []string{id}, "action": batchAction}
		if batchAction == "trash" {
			body["retention_days"] = *retentionDays
		}
		if batchAction == "delete" {
			body["confirm_permanent"] = true
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/calls/batch", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlBatchResult(stdout, options, result)
	default:
		return controlErrorf(controlExitInvalid, "未知 call 子命令 %q", action)
	}
}

func callStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "partial", "failed", "cancelled", "unknown":
		return true
	default:
		return false
	}
}

func directStringField(value any, key string) (string, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	text, ok := object[key].(string)
	return text, ok
}

func writeControlExport(stdout io.Writer, options resolvedControlOptions, path string, result any, listKey string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return writeControlOutput(stdout, options, result, listKey)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return controlWrap(controlExitService, "编码导出内容失败", err)
	}
	data = append(data, '\n')
	if err = os.WriteFile(path, data, 0o600); err != nil {
		return controlWrap(controlExitService, "写入导出文件失败", err)
	}
	return writeControlOutput(stdout, options, map[string]any{"ok": true, "output": path, "bytes": len(data)}, "")
}
