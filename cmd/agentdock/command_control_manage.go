package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func runPermissionControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock permission <show|effective|set> ...")
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock permission "+action, stderr, &raw)
	scope := flags.String("scope", "", "权限作用域：global、workspace 或 conversation")
	scopeID := flags.String("scope-id", "", "作用域 ID")
	mode := flags.String("mode", "", "权限模式：readonly、rules 或 full")
	expectedRevision := flags.Uint64("expected-revision", 0, "读取到的策略 revision")
	confirmFull := flags.Bool("confirm-full", false, "显式确认 full 模式")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 permission 参数失败", err)
	}
	if flags.NArg() != 0 {
		return controlErrorf(controlExitInvalid, "permission 不接受位置参数")
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	switch action {
	case "show", "effective":
		query := url.Values{}
		if options.workspaceID != "" {
			query.Set("workspace_id", options.workspaceID)
		}
		if options.conversation != "" {
			query.Set("conversation_id", options.conversation)
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/permissions/effective", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "set":
		selectedScope := strings.ToLower(strings.TrimSpace(*scope))
		selectedMode := strings.ToLower(strings.TrimSpace(*mode))
		if selectedScope != "global" && selectedScope != "workspace" && selectedScope != "conversation" {
			return controlErrorf(controlExitInvalid, "permission set 的 --scope 必须是 global、workspace 或 conversation")
		}
		if selectedMode != "readonly" && selectedMode != "rules" && selectedMode != "full" {
			return controlErrorf(controlExitInvalid, "permission set 的 --mode 必须是 readonly、rules 或 full")
		}
		id := strings.TrimSpace(*scopeID)
		if selectedScope == "workspace" && id == "" {
			id = options.workspaceID
		}
		if selectedScope == "conversation" && id == "" {
			id = options.conversation
		}
		if selectedScope != "global" && id == "" {
			return controlErrorf(controlExitInvalid, "非 global 作用域需要 --scope-id 或相应默认选择器")
		}
		if *expectedRevision == 0 {
			return controlErrorf(controlExitInvalid, "permission set 需要 --expected-revision 防止覆盖并发修改")
		}
		if selectedMode == "full" && !*confirmFull {
			return controlErrorf(controlExitInvalid, "设置 full 模式需要 --confirm-full")
		}
		body := map[string]any{"scope": selectedScope, "scope_id": id, "mode": selectedMode, "expected_revision": *expectedRevision}
		if selectedMode == "full" {
			body["confirm_full"] = true
		}
		result, requestErr := client.request(ctx, http.MethodPost, "/internal/runtime/permissions", nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	default:
		return controlErrorf(controlExitInvalid, "未知 permission 子命令 %q", action)
	}
}

func runDoctorControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		args = append([]string{"report"}, args...)
	}
	if strings.ToLower(args[0]) != "report" {
		return controlErrorf(controlExitInvalid, "用法：agentdock doctor report [控制选项]")
	}
	var raw controlOptions
	flags := newControlFlagSet("agentdock doctor report", stderr, &raw)
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 doctor 参数失败", err)
	}
	if flags.NArg() != 0 {
		return controlErrorf(controlExitInvalid, "doctor report 不接受位置参数")
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	type check struct {
		name  string
		path  string
		query url.Values
	}
	checks := []check{
		{name: "runtime", path: "/internal/runtime/status"},
		{name: "workspaces", path: "/internal/runtime/workspaces"},
		{name: "permissions", path: "/internal/runtime/permissions/effective", query: selectorQuery(options)},
	}
	report := map[string]any{"ok": true, "checks": map[string]any{}}
	results := report["checks"].(map[string]any)
	failed := 0
	for _, item := range checks {
		value, requestErr := client.request(ctx, http.MethodGet, item.path, item.query, nil)
		if requestErr != nil {
			results[item.name] = map[string]any{"ok": false, "error": requestErr.Error(), "exit_code": commandExitCode(requestErr)}
			failed++
			continue
		}
		results[item.name] = map[string]any{"ok": true, "result": value}
	}
	if failed != 0 {
		report["ok"] = false
		report["failed"] = failed
	}
	if err := writeControlOutput(stdout, options, report, ""); err != nil {
		return err
	}
	if failed != 0 {
		return markControlErrorReported(controlErrorf(controlExitPartial, "doctor report 有 %d 项检查失败", failed))
	}
	return nil
}

func findAnyField(value any, key string) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if result, ok := typed[key]; ok {
			return result, true
		}
		for _, nested := range typed {
			if result, ok := findAnyField(nested, key); ok {
				return result, true
			}
		}
	case []any:
		for _, nested := range typed {
			if result, ok := findAnyField(nested, key); ok {
				return result, true
			}
		}
	}
	return nil, false
}

func findStringField(value any, key string) (string, bool) {
	found, ok := findAnyField(value, key)
	if !ok {
		return "", false
	}
	text, ok := found.(string)
	return text, ok
}

func findUintField(value any, key string) (uint64, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[key]; ok {
			switch number := raw.(type) {
			case json.Number:
				parsed, err := strconv.ParseUint(number.String(), 10, 64)
				return parsed, err == nil
			case float64:
				if number >= 0 && number == float64(uint64(number)) {
					return uint64(number), true
				}
			case string:
				parsed, err := strconv.ParseUint(number, 10, 64)
				return parsed, err == nil
			}
		}
		for _, nested := range typed {
			if result, ok := findUintField(nested, key); ok {
				return result, true
			}
		}
	case []any:
		for _, nested := range typed {
			if result, ok := findUintField(nested, key); ok {
				return result, true
			}
		}
	}
	return 0, false
}

func writeControlBatchResult(stdout io.Writer, options resolvedControlOptions, result any) error {
	if err := writeControlOutput(stdout, options, result, "items"); err != nil {
		return err
	}
	if object, ok := result.(map[string]any); ok {
		if status, _ := object["status"].(string); status == "partial" {
			return markControlErrorReported(controlErrorf(controlExitPartial, "批量操作结果为 %s", status))
		} else if status == "failed" {
			return markControlErrorReported(controlErrorf(controlExitFailure, "批量操作结果为 %s", status))
		}
	}
	return nil
}

func newSubmissionID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", controlWrap(controlExitService, "生成 submission_id 失败", err)
	}
	return "cli_" + hex.EncodeToString(value[:]), nil
}
