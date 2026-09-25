package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxInsertionInputBytes = 64 * 1024

func runInsertionControl(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return controlErrorf(controlExitInvalid, "用法：agentdock insertion <list|show|send|wait|cancel|retry> ...")
	}
	action := strings.ToLower(args[0])
	if action == "enqueue" {
		action = "send"
	}
	var raw controlOptions
	flags := newControlFlagSet("agentdock insertion "+action, stderr, &raw)
	text := flags.String("text", "", "插入文本")
	textFile := flags.String("file", "", "从 UTF-8 文件读取插入文本；- 表示 stdin")
	submissionID := flags.String("submission-id", "", "幂等提交 ID；省略时自动生成")
	waitTimeout := flags.Duration("wait-timeout", 5*time.Minute, "等待终态的最长时间")
	pollInterval := flags.Duration("poll-interval", time.Second, "等待查询间隔")
	if err := flags.Parse(args[1:]); err != nil {
		return controlWrap(controlExitInvalid, "解析 insertion 参数失败", err)
	}
	options, err := resolveControlOptions(raw)
	if err != nil {
		return err
	}
	positionals := flags.Args()
	conversation := strings.TrimSpace(options.conversation)
	if conversation == "" {
		if len(positionals) == 0 {
			return controlErrorf(controlExitInvalid, "insertion 需要 conversation_id 位置参数或 --conversation")
		}
		conversation = strings.TrimSpace(positionals[0])
		positionals = positionals[1:]
	}
	client := newControlClient(options, stderr)
	base := "/internal/runtime/conversations/" + pathSegment(conversation) + "/insertions"
	switch action {
	case "list":
		if len(positionals) != 0 {
			return controlErrorf(controlExitInvalid, "insertion list 不接受 insertion_id")
		}
		result, requestErr := client.request(ctx, http.MethodGet, base, nil, nil)
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "insertions")
	case "send":
		if len(positionals) != 0 || (strings.TrimSpace(*text) == "") == (strings.TrimSpace(*textFile) == "") {
			return controlErrorf(controlExitInvalid, "insertion send 必须且只能提供 --text 或 --file")
		}
		message := strings.TrimSpace(*text)
		if strings.TrimSpace(*textFile) != "" {
			data, readErr := readBoundedInsertionInput(strings.TrimSpace(*textFile))
			if readErr != nil {
				return readErr
			}
			message = strings.TrimSpace(string(data))
		}
		if message == "" {
			return controlErrorf(controlExitInvalid, "插入文本不能为空")
		}
		id := strings.TrimSpace(*submissionID)
		if id == "" {
			id, err = newSubmissionID()
			if err != nil {
				return err
			}
		}
		result, requestErr := client.request(ctx, http.MethodPost, base, nil, map[string]any{"submission_id": id, "text": message})
		if requestErr != nil {
			return requestErr
		}
		return writeControlOutput(stdout, options, result, "")
	case "show", "wait", "cancel", "retry":
		if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
			return controlErrorf(controlExitInvalid, "insertion %s 需要一个 insertion_id", action)
		}
		insertionID := strings.TrimSpace(positionals[0])
		if action == "cancel" || action == "retry" {
			result, requestErr := client.request(ctx, http.MethodPost, base+"/"+pathSegment(insertionID)+"/"+action, nil, map[string]any{})
			if requestErr != nil {
				return requestErr
			}
			return writeControlOutput(stdout, options, result, "insertions")
		}
		fetch := func(fetchCtx context.Context) (map[string]any, error) {
			result, requestErr := client.request(fetchCtx, http.MethodGet, base, nil, nil)
			if requestErr != nil {
				return nil, requestErr
			}
			item, ok := findRecordByID(result, insertionID)
			if !ok {
				return nil, &controlError{code: controlExitNotFound, stableCode: "INSERTION_NOT_FOUND", message: "未找到 insertion_id " + insertionID}
			}
			return item, nil
		}
		if action == "show" {
			item, fetchErr := fetch(ctx)
			if fetchErr != nil {
				return fetchErr
			}
			return writeControlOutput(stdout, options, item, "")
		}
		if *waitTimeout <= 0 || *waitTimeout > 24*time.Hour || *pollInterval < 100*time.Millisecond || *pollInterval > time.Minute {
			return controlErrorf(controlExitInvalid, "insertion wait 需要大于 0 且不超过 24h 的 wait-timeout，以及 100ms..1m 的 poll-interval")
		}
		waitCtx, cancel := context.WithTimeout(ctx, *waitTimeout)
		defer cancel()
		for {
			item, fetchErr := fetch(waitCtx)
			if fetchErr != nil {
				if waitCtx.Err() != nil {
					return &controlError{code: controlExitTimeout, stableCode: "WAIT_TIMEOUT", message: "等待插入终态超时"}
				}
				return fetchErr
			}
			status, ok := findStringField(item, "status")
			if !ok {
				return controlErrorf(controlExitService, "插入记录未返回 status")
			}
			if insertionTerminalStatus(status) {
				if outputErr := writeControlOutput(stdout, options, item, ""); outputErr != nil {
					return outputErr
				}
				if strings.EqualFold(status, "acknowledged") {
					return nil
				}
				return markControlErrorReported(&controlError{code: controlExitConflict, stableCode: "INSERTION_" + strings.ToUpper(status), message: "插入以非确认终态结束: " + status})
			}
			timer := time.NewTimer(*pollInterval)
			select {
			case <-waitCtx.Done():
				timer.Stop()
				return &controlError{code: controlExitTimeout, stableCode: "WAIT_TIMEOUT", message: "等待插入终态超时"}
			case <-timer.C:
			}
		}
	default:
		return controlErrorf(controlExitInvalid, "未知 insertion 子命令 %q", action)
	}
}

func readBoundedInsertionInput(path string) ([]byte, error) {
	var reader io.Reader
	var closeFile *os.File
	if path == "-" {
		reader = os.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return nil, controlWrap(controlExitInvalid, "读取插入文件失败", err)
		}
		closeFile = file
		reader = file
	}
	if closeFile != nil {
		defer closeFile.Close()
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxInsertionInputBytes+1))
	if err != nil {
		return nil, controlWrap(controlExitInvalid, "读取插入输入失败", err)
	}
	if len(data) > maxInsertionInputBytes {
		return nil, controlErrorf(controlExitInvalid, "插入输入超过 64 KiB")
	}
	return data, nil
}

func findRecordByID(value any, id string) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"insertion_id", "id"} {
			if current, _ := typed[key].(string); current == id {
				return typed, true
			}
		}
		for _, nested := range typed {
			if item, ok := findRecordByID(nested, id); ok {
				return item, true
			}
		}
	case []any:
		for _, nested := range typed {
			if item, ok := findRecordByID(nested, id); ok {
				return item, true
			}
		}
	}
	return nil, false
}

func insertionTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "acknowledged", "expired", "cancelled", "target_changed", "delivery_unknown":
		return true
	default:
		return false
	}
}
