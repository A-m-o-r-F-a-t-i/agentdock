package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func runApprovalControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock approval <list|history|show|wait|approve|reject> ...")
	}
	action := strings.ToLower(args[0])
	var raw controlOptions
	flags := newControlFlagSet("agentdock approval "+action, stderr, &raw)
	status := flags.String("status", "", "审批状态筛选")
	limit := flags.Int("limit", 100, "最大返回数量（1-200）")
	offset := flags.Int("offset", 0, "分页偏移")
	allowWorkspace := flags.Bool("allow-workspace", false, "批准时为工作区添加持久允许")
	waitTimeout := flags.Duration("wait-timeout", 5*time.Minute, "等待非 pending 状态的最长时间")
	pollInterval := flags.Duration("poll-interval", time.Second, "等待查询间隔")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 approval 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	client := newControlClient(options, stderr)
	positionals := flags.Args()
	if action == "list" || action == "history" {
		if len(positionals) != 0 || *limit < 1 || *limit > 200 || *offset < 0 || *offset > 20000 {
			return controlErrorf(controlExitInvalid, "approval list/history 分页参数无效")
		}
		query := url.Values{"limit": {strconv.Itoa(*limit)}, "offset": {strconv.Itoa(*offset)}}
		if value := strings.TrimSpace(*status); value != "" {
			query.Set("status", value)
		}
		result, requestErr := client.request(ctx, http.MethodGet, "/internal/runtime/approvals", query, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "approvals")
	}
	id, parseErr := oneIdentifier(positionals, "", "approval_id")
	if parseErr != nil {
		return parseErr
	}
	base := "/internal/runtime/approvals/" + pathSegment(id)
	switch action {
	case "show":
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "wait":
		if *waitTimeout <= 0 || *waitTimeout > 24*time.Hour || *pollInterval < 100*time.Millisecond || *pollInterval > time.Minute {
			return controlErrorf(controlExitInvalid, "approval wait 需要大于 0 且不超过 24h 的 wait-timeout，以及 100ms..1m 的 poll-interval")
		}
		waitCtx, cancel := context.WithTimeout(ctx, *waitTimeout)
		defer cancel()
		for {
			result, requestErr := client.request(waitCtx, http.MethodGet, base, nil, nil)
			if requestErr != nil {
				if waitCtx.Err() != nil {
					return &controlError{code: controlExitTimeout, stableCode: "WAIT_TIMEOUT", message: "等待审批结果超时"}
				}
				return requestErr
			}
			statusValue, ok := findStringField(result, "status")
			if !ok {
				return controlErrorf(controlExitService, "审批详情未返回 status")
			}
			if strings.ToLower(statusValue) != "pending" {
				return writeControlOutput(stdout, options, result, "")
			}
			timer := time.NewTimer(*pollInterval)
			select {
			case <-waitCtx.Done():
				timer.Stop()
				return &controlError{code: controlExitTimeout, stableCode: "WAIT_TIMEOUT", message: "等待审批结果超时"}
			case <-timer.C:
			}
		}
	case "approve", "reject":
		body := map[string]any{"allow_workspace": action == "approve" && *allowWorkspace}
		result, requestErr := client.request(ctx, http.MethodPost, base+"/"+action, nil, body)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	default:
		return controlErrorf(controlExitInvalid, "未知 approval 子命令 %q", action)
	}
}
