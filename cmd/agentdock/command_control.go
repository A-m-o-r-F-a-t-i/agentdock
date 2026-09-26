package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	controlExitFailure     = 1
	controlExitInvalid     = 2
	controlExitNotFound    = 3
	controlExitPermission  = 4
	controlExitService     = 5
	controlExitApproval    = 6
	controlExitConflict    = 7
	controlExitUnsupported = 8
	controlExitPartial     = 9
	controlExitTimeout     = 10
	controlExitInterrupted = 130
	controlResponseLimit   = 8 << 20
)

type controlError struct {
	code       int
	stableCode string
	message    string
	cause      error
	reported   bool
}

func (e *controlError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return e.message + ": " + e.cause.Error()
	}
	return e.message
}

func (e *controlError) Unwrap() error { return e.cause }

func controlErrorf(code int, format string, args ...any) error {
	return &controlError{code: code, stableCode: controlStableCode(code), message: fmt.Sprintf(format, args...)}
}

func controlWrap(code int, message string, err error) error {
	return &controlError{code: code, stableCode: controlStableCode(code), message: message, cause: err}
}

func controlStableCode(code int) string {
	switch code {
	case controlExitFailure:
		return "OPERATION_FAILED"
	case controlExitInvalid:
		return "INVALID_ARGUMENT"
	case controlExitNotFound:
		return "NOT_FOUND"
	case controlExitPermission:
		return "PERMISSION_DENIED"
	case controlExitService:
		return "SERVICE_UNAVAILABLE"
	case controlExitApproval:
		return "APPROVAL_REQUIRED"
	case controlExitConflict:
		return "CONFLICT"
	case controlExitUnsupported:
		return "UNSUPPORTED"
	case controlExitPartial:
		return "PARTIAL_FAILURE"
	case controlExitTimeout:
		return "TIMEOUT"
	case controlExitInterrupted:
		return "INTERRUPTED"
	default:
		return "COMMAND_FAILED"
	}
}

func markControlErrorReported(err error) error {
	var target *controlError
	if errors.As(err, &target) {
		target.reported = true
		return err
	}
	return &controlError{code: commandExitCode(err), stableCode: "COMMAND_FAILED", message: err.Error(), cause: err, reported: true}
}

func controlErrorReported(err error) bool {
	var target *controlError
	return errors.As(err, &target) && target.reported
}

func renderControlCommandError(args []string, output io.Writer, err error) error {
	if err == nil || !controlMachineErrorRequested(args) || controlErrorReported(err) {
		return err
	}
	var target *controlError
	if !errors.As(err, &target) {
		target = &controlError{code: commandExitCode(err), stableCode: "COMMAND_FAILED", message: err.Error(), cause: err}
	}
	code := target.stableCode
	if code == "" {
		code = controlStableCode(target.code)
	}
	envelope := map[string]any{"ok": false, "code": code, "error": target.Error(), "exit_code": commandExitCode(target)}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if encodeErr := encoder.Encode(envelope); encodeErr != nil {
		return controlWrap(controlExitService, "写入错误 JSON 失败", encodeErr)
	}
	target.reported = true
	return target
}

func controlMachineErrorRequested(args []string) bool {
	format := strings.ToLower(strings.TrimSpace(os.Getenv("AGENTDOCK_OUTPUT_FORMAT")))
	for index, argument := range args {
		switch {
		case argument == "--json", argument == "--jsonl":
			return true
		case strings.HasPrefix(argument, "--format="):
			format = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(argument, "--format=")))
		case argument == "--format" && index+1 < len(args):
			format = strings.ToLower(strings.TrimSpace(args[index+1]))
		}
	}
	return format == "json" || format == "jsonl"
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return controlExitInterrupted
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return controlExitTimeout
	}
	var target *controlError
	if errors.As(err, &target) && target.code != 0 {
		return target.code
	}
	return 1
}

type controlOptions struct {
	endpoint     string
	tokenFile    string
	authMode     string
	profile      string
	workspaceID  string
	project      string
	taskID       string
	threadID     string
	conversation string
	callID       string
	format       string
	timeout      time.Duration
	jsonOutput   bool
	jsonLines    bool
	noColor      bool
	quiet        bool
	verbose      bool
}

type controlProfile struct {
	Endpoint       string `json:"endpoint"`
	TokenFile      string `json:"token_file"`
	AuthMode       string `json:"auth_mode"`
	WorkspaceID    string `json:"workspace_id"`
	Project        string `json:"project"`
	TaskID         string `json:"task_id"`
	ThreadID       string `json:"thread_id"`
	ConversationID string `json:"conversation_id"`
	CallID         string `json:"call_id"`
	OutputFormat   string `json:"output_format"`
	Timeout        string `json:"timeout"`
}

type controlConfigFile struct {
	DefaultProfile string                    `json:"default_profile"`
	Profiles       map[string]controlProfile `json:"profiles"`
}

type resolvedControlOptions struct {
	controlOptions
	token string
}

func addControlFlags(flags *flag.FlagSet, options *controlOptions) {
	flags.StringVar(&options.endpoint, "endpoint", "", "AgentDock Core 地址，例如 http://127.0.0.1:8765")
	flags.StringVar(&options.tokenFile, "token-file", "", "Bearer/OAuth token 文件；不会从命令行接收 token 值")
	flags.StringVar(&options.authMode, "auth-mode", "", "认证模式：auto、bearer、oauth 或 none")
	flags.StringVar(&options.profile, "profile", "", "本地 CLI 配置 Profile")
	flags.StringVar(&options.workspaceID, "workspace", "", "默认 workspace_id")
	flags.StringVar(&options.project, "project", "", "默认项目筛选")
	flags.StringVar(&options.taskID, "task", "", "默认 task_id")
	flags.StringVar(&options.threadID, "thread", "", "默认 thread_id")
	flags.StringVar(&options.conversation, "conversation", "", "默认 conversation_id")
	flags.StringVar(&options.callID, "call", "", "默认 call_id")
	flags.StringVar(&options.format, "format", "", "输出格式：human、json 或 jsonl")
	flags.DurationVar(&options.timeout, "timeout", 0, "请求超时，例如 10s、2m")
	flags.BoolVar(&options.jsonOutput, "json", false, "输出单个 JSON 值")
	flags.BoolVar(&options.jsonLines, "jsonl", false, "输出 JSON Lines")
	flags.BoolVar(&options.noColor, "no-color", false, "禁用颜色（当前稳定输出默认无颜色）")
	flags.BoolVar(&options.quiet, "quiet", false, "成功时不输出")
	flags.BoolVar(&options.verbose, "verbose", false, "向 stderr 输出脱敏请求信息")
}

func resolveControlOptions(raw controlOptions) (resolvedControlOptions, error) {
	if raw.jsonOutput && raw.jsonLines {
		return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "--json 与 --jsonl 不能同时使用")
	}
	config, configPath, err := readControlConfig()
	if err != nil {
		return resolvedControlOptions{}, err
	}
	profileName := firstNonEmpty(raw.profile, os.Getenv("AGENTDOCK_PROFILE"), config.DefaultProfile)
	profile := controlProfile{}
	if profileName != "" {
		selected, ok := config.Profiles[profileName]
		if !ok {
			location := configPath
			if location == "" {
				location = "CLI 配置"
			}
			return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "CLI Profile %q 不存在于 %s", profileName, location)
		}
		profile = selected
	}
	format := strings.ToLower(firstNonEmpty(raw.format, os.Getenv("AGENTDOCK_OUTPUT_FORMAT"), profile.OutputFormat, "human"))
	if raw.jsonOutput || raw.jsonLines {
		if raw.format != "" {
			return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "--format 不能与 --json 或 --jsonl 同时使用")
		}
		if raw.jsonOutput {
			format = "json"
		} else {
			format = "jsonl"
		}
	}
	switch format {
	case "human", "text":
		raw.jsonOutput, raw.jsonLines = false, false
	case "json":
		raw.jsonOutput, raw.jsonLines = true, false
	case "jsonl":
		raw.jsonOutput, raw.jsonLines = false, true
	default:
		return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "不支持的输出格式 %q", format)
	}

	port := strings.TrimSpace(os.Getenv("AGENTDOCK_PORT"))
	if port == "" {
		port = "8765"
	}
	endpoint := firstNonEmpty(raw.endpoint, os.Getenv("AGENTDOCK_ENDPOINT"), profile.Endpoint, "http://127.0.0.1:"+port)
	normalizedEndpoint, err := normalizeControlEndpoint(endpoint)
	if err != nil {
		return resolvedControlOptions{}, err
	}

	timeout := raw.timeout
	if timeout == 0 {
		timeoutText := firstNonEmpty(os.Getenv("AGENTDOCK_CLI_TIMEOUT"), profile.Timeout, "15s")
		timeout, err = time.ParseDuration(timeoutText)
		if err != nil || timeout <= 0 || timeout > 24*time.Hour {
			return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "CLI timeout 无效: %q", timeoutText)
		}
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "--timeout 必须大于 0 且不超过 24h")
	}

	authMode := strings.ToLower(firstNonEmpty(raw.authMode, os.Getenv("AGENTDOCK_AUTH_MODE"), profile.AuthMode, "auto"))
	switch authMode {
	case "auto", "bearer", "oauth", "none":
	default:
		return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "不支持的 auth-mode %q", authMode)
	}
	tokenFile := firstNonEmpty(raw.tokenFile, os.Getenv("AGENTDOCK_TOKEN_FILE"), profile.TokenFile)
	token := ""
	if tokenFile != "" {
		tokenFile, err = expandUserPath(tokenFile)
		if err != nil {
			return resolvedControlOptions{}, err
		}
		data, readErr := os.ReadFile(tokenFile)
		if readErr != nil {
			return resolvedControlOptions{}, controlWrap(controlExitPermission, "读取 token 文件失败", readErr)
		}
		if len(data) > 64*1024 {
			return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "token 文件超过 64 KiB")
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return resolvedControlOptions{}, controlErrorf(controlExitInvalid, "token 文件为空")
		}
	} else {
		// 环境变量是兼容入口；不会被回显或写入子进程参数。
		token = strings.TrimSpace(os.Getenv("AGENTDOCK_AUTH_TOKEN"))
	}
	if authMode != "none" && authMode != "auto" && token == "" {
		return resolvedControlOptions{}, controlErrorf(controlExitPermission, "auth-mode=%s 需要 --token-file、AGENTDOCK_TOKEN_FILE 或 AGENTDOCK_AUTH_TOKEN", authMode)
	}
	if authMode == "none" {
		token = ""
	}

	resolved := raw
	resolved.endpoint = normalizedEndpoint
	resolved.tokenFile = tokenFile
	resolved.authMode = authMode
	resolved.profile = profileName
	resolved.workspaceID = firstNonEmpty(raw.workspaceID, os.Getenv("AGENTDOCK_WORKSPACE_ID"), profile.WorkspaceID)
	resolved.project = firstNonEmpty(raw.project, os.Getenv("AGENTDOCK_PROJECT"), profile.Project)
	resolved.taskID = firstNonEmpty(raw.taskID, os.Getenv("AGENTDOCK_TASK_ID"), profile.TaskID)
	resolved.threadID = firstNonEmpty(raw.threadID, os.Getenv("AGENTDOCK_THREAD_ID"), profile.ThreadID)
	resolved.conversation = firstNonEmpty(raw.conversation, os.Getenv("AGENTDOCK_CONVERSATION_ID"), profile.ConversationID)
	resolved.callID = firstNonEmpty(raw.callID, os.Getenv("AGENTDOCK_CALL_ID"), profile.CallID)
	resolved.format = format
	resolved.noColor = raw.noColor || os.Getenv("NO_COLOR") != ""
	resolved.timeout = timeout
	return resolvedControlOptions{controlOptions: resolved, token: token}, nil
}

func readControlConfig() (controlConfigFile, string, error) {
	path := strings.TrimSpace(os.Getenv("AGENTDOCK_CLI_CONFIG"))
	if path == "" {
		home := strings.TrimSpace(os.Getenv("AGENTDOCK_HOME"))
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err == nil {
				home = filepath.Join(userHome, ".agentdock")
			}
		}
		if home != "" {
			path = filepath.Join(home, "cli.json")
		}
	}
	if path == "" {
		return controlConfigFile{}, "", nil
	}
	expanded, err := expandUserPath(path)
	if err != nil {
		return controlConfigFile{}, "", err
	}
	data, err := os.ReadFile(expanded)
	if errors.Is(err, os.ErrNotExist) {
		return controlConfigFile{}, "", nil
	}
	if err != nil {
		return controlConfigFile{}, "", controlWrap(controlExitPermission, "读取 CLI 配置失败", err)
	}
	if len(data) > 1<<20 {
		return controlConfigFile{}, "", controlErrorf(controlExitInvalid, "CLI 配置超过 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config controlConfigFile
	if err := decoder.Decode(&config); err != nil {
		return controlConfigFile{}, "", controlWrap(controlExitInvalid, "解析 CLI 配置失败", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return controlConfigFile{}, "", controlErrorf(controlExitInvalid, "CLI 配置必须只包含一个 JSON 对象")
	}
	return config, expanded, nil
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "~" {
		if path == "" {
			return "", nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", controlWrap(controlExitInvalid, "解析用户目录失败", err)
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", controlWrap(controlExitInvalid, "解析用户目录失败", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return filepath.Clean(path), nil
}

func normalizeControlEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", controlErrorf(controlExitInvalid, "endpoint 必须是绝对 HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", controlErrorf(controlExitInvalid, "endpoint 只支持 http 或 https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", controlErrorf(controlExitInvalid, "endpoint 不能包含账号、路径、查询或片段")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

type controlClient struct {
	endpoint string
	token    string
	verbose  bool
	stderr   io.Writer
	http     *http.Client
}

func newControlClient(options resolvedControlOptions, stderr io.Writer) *controlClient {
	return &controlClient{
		endpoint: options.endpoint,
		token:    options.token,
		verbose:  options.verbose,
		stderr:   stderr,
		http:     &http.Client{Timeout: options.timeout},
	}
}

func (c *controlClient) request(ctx context.Context, method, path string, query url.Values, body any) (any, error) {
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, controlWrap(controlExitInvalid, "编码请求失败", err)
		}
		input = bytes.NewReader(encoded)
	}
	requestURL := c.endpoint + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, input)
	if err != nil {
		return nil, controlWrap(controlExitInvalid, "创建请求失败", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.verbose {
		fmt.Fprintf(c.stderr, "control request: %s %s%s\n", method, c.endpoint, path)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, controlWrap(controlExitService, "连接 AgentDock Core 失败", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, controlResponseLimit+1))
	if err != nil {
		return nil, controlWrap(controlExitService, "读取 Core 响应失败", err)
	}
	if len(data) > controlResponseLimit {
		return nil, controlErrorf(controlExitService, "Core 响应超过 %d MiB", controlResponseLimit>>20)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeControlHTTPError(response.StatusCode, data)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{"ok": true}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, controlWrap(controlExitService, "Core 返回了无效 JSON", err)
	}
	return result, nil
}

func decodeControlHTTPError(status int, data []byte) error {
	var envelope struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &envelope)
	message := strings.TrimSpace(envelope.Error)
	if message == "" {
		message = strings.TrimSpace(http.StatusText(status))
	}
	stableCode := strings.TrimSpace(envelope.Code)
	if stableCode == "" {
		stableCode = "HTTP_" + strconv.Itoa(status)
	}
	code := controlExitService
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = controlExitInvalid
	case http.StatusNotFound:
		code = controlExitNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		code = controlExitPermission
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		code = controlExitTimeout
	case http.StatusConflict, http.StatusPreconditionFailed, http.StatusTooManyRequests:
		code = controlExitConflict
	case http.StatusMultiStatus:
		code = controlExitPartial
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		code = controlExitUnsupported
	}
	switch strings.ToUpper(stableCode) {
	case "APPROVAL_REQUIRED", "APPROVAL_PENDING", "WAITING_APPROVAL":
		code = controlExitApproval
	case "NOT_FOUND", "RESOURCE_NOT_FOUND", "TASK_NOT_FOUND", "CONVERSATION_NOT_FOUND", "CALL_NOT_FOUND", "WORKSPACE_NOT_FOUND":
		code = controlExitNotFound
	case "CAPABILITY_UNSUPPORTED", "NOT_IMPLEMENTED", "UNSUPPORTED":
		code = controlExitUnsupported
	case "PARTIAL_FAILURE", "PARTIAL_RESULT":
		code = controlExitPartial
	case "TIMEOUT", "WAIT_TIMEOUT", "DEADLINE_EXCEEDED":
		code = controlExitTimeout
	}
	return &controlError{code: code, stableCode: stableCode, message: message}
}

func writeControlOutput(output io.Writer, options resolvedControlOptions, result any, listKey string) error {
	if options.quiet {
		return nil
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if options.jsonLines {
		for _, item := range controlOutputItems(result, listKey) {
			if err := encoder.Encode(item); err != nil {
				return controlWrap(controlExitService, "写入 JSONL 失败", err)
			}
		}
		return nil
	}
	if !options.jsonOutput {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		return controlWrap(controlExitService, "写入输出失败", err)
	}
	return nil
}

func controlOutputItems(result any, listKey string) []any {
	if items, ok := result.([]any); ok {
		return items
	}
	if object, ok := result.(map[string]any); ok && listKey != "" {
		if items, ok := object[listKey].([]any); ok {
			return items
		}
	}
	return []any{result}
}

func pathSegment(value string) string { return url.PathEscape(strings.TrimSpace(value)) }

func setPositiveQuery(query url.Values, key string, value int64) {
	if value > 0 {
		query.Set(key, strconv.FormatInt(value, 10))
	}
}

func isControlSkillCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "list" || args[0] == "show" || args[0] == "status")
}

func isControlPluginCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "show", "status", "test":
		return true
	case "list":
		for _, arg := range args[1:] {
			if arg == "--home" || strings.HasPrefix(arg, "--home=") {
				return false
			}
		}
		return true
	default:
		return false
	}
}
