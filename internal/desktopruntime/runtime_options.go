package desktopruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentconfig "github.com/uvwt/agentdock/internal/config"
)

// RuntimeOptions contains non-secret settings previously accessible only through
// environment variables. The desktop host persists and supplies them explicitly.
type RuntimeOptions struct {
	DefaultDir            string            `json:"default_dir"`
	AgentsAutoLoad        bool              `json:"agents_autoload"`
	InstructionsFile      string            `json:"instructions_file"`
	BrowserExecutablePath string            `json:"browser_executable_path"`
	TrustedProxyCIDRs     []string          `json:"trusted_proxy_cidrs"`
	CommandEnvFromEnv     map[string]string `json:"command_env_from_env"`
	ACPMaxPrompts         int               `json:"acp_max_concurrent_prompts"`
	ACPInteractionMS      int               `json:"acp_interaction_timeout_ms"`
}

type RuntimeOptionsView struct {
	Options       RuntimeOptions `json:"options"`
	AgentDockHome string         `json:"agentdock_home"`
	SettingsPath  string         `json:"settings_path"`
	ManifestPath  string         `json:"manifest_path"`
}

func defaultRuntimeOptions() RuntimeOptions {
	return RuntimeOptions{AgentsAutoLoad: true, ACPMaxPrompts: 2, ACPInteractionMS: 300000,
		TrustedProxyCIDRs: []string{}, CommandEnvFromEnv: map[string]string{}}
}

func (options *RuntimeOptions) UnmarshalJSON(data []byte) error {
	type plain RuntimeOptions
	value := plain(defaultRuntimeOptions())
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("runtime options must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*options = RuntimeOptions(value)
	return nil
}

// normalizeRuntimeOptions performs no writes and is run before a service stop.
func normalizeRuntimeOptions(options RuntimeOptions) (RuntimeOptions, error) {
	options.DefaultDir = strings.TrimSpace(options.DefaultDir)
	if options.DefaultDir == "" || !filepath.IsAbs(options.DefaultDir) {
		return options, errors.New("默认全局工作区必须是绝对目录路径")
	}
	options.DefaultDir = filepath.Clean(options.DefaultDir)
	if info, err := os.Stat(options.DefaultDir); err == nil && !info.IsDir() {
		return options, errors.New("默认全局工作区不能指向文件")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return options, fmt.Errorf("读取默认全局工作区失败: %w", err)
	}
	options.InstructionsFile = strings.TrimSpace(options.InstructionsFile)
	if options.InstructionsFile != "" {
		options.InstructionsFile = filepath.Clean(options.InstructionsFile)
		if _, err := agentconfig.ReadInstructionsFile(options.InstructionsFile); err != nil {
			return options, err
		}
	}
	options.BrowserExecutablePath = strings.TrimSpace(options.BrowserExecutablePath)
	if options.BrowserExecutablePath != "" {
		options.BrowserExecutablePath = filepath.Clean(options.BrowserExecutablePath)
		if !filepath.IsAbs(options.BrowserExecutablePath) {
			return options, errors.New("浏览器程序必须使用绝对路径")
		}
		info, err := os.Stat(options.BrowserExecutablePath)
		if err != nil {
			return options, fmt.Errorf("读取浏览器程序失败: %w", err)
		}
		if !info.Mode().IsRegular() {
			return options, errors.New("浏览器程序必须是普通文件")
		}
	}
	if options.ACPMaxPrompts < 1 || options.ACPMaxPrompts > 8 {
		return options, errors.New("ACP 并发提示数必须在 1—8 之间")
	}
	if options.ACPInteractionMS < 1000 || options.ACPInteractionMS > 3600000 {
		return options, errors.New("ACP 交互超时必须在 1000—3600000 毫秒之间")
	}
	if len(options.TrustedProxyCIDRs) > 64 {
		return options, errors.New("可信代理网段最多 64 项")
	}
	networks := []string{}
	seen := map[string]bool{}
	for _, raw := range options.TrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return options, fmt.Errorf("可信代理 CIDR 无效: %w", err)
		}
		value := network.String()
		if !seen[value] {
			networks = append(networks, value)
			seen[value] = true
		}
	}
	options.TrustedProxyCIDRs = networks
	if err := agentconfig.ValidateEnvironmentMapping(options.CommandEnvFromEnv); err != nil {
		return options, fmt.Errorf("命令环境变量引用无效: %w", err)
	}
	return options, nil
}

func (options RuntimeOptions) environment() (map[string]string, error) {
	mapping, err := json.Marshal(options.CommandEnvFromEnv)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENTDOCK_DEFAULT_DIR":                options.DefaultDir,
		"AGENTDOCK_AGENTS_AUTOLOAD":            strconv.FormatBool(options.AgentsAutoLoad),
		"AGENTDOCK_INSTRUCTIONS_FILE":          options.InstructionsFile,
		"AGENTDOCK_BROWSER_EXECUTABLE_PATH":    options.BrowserExecutablePath,
		"AGENTDOCK_TRUSTED_PROXY_CIDRS":        strings.Join(options.TrustedProxyCIDRs, ","),
		"AGENTDOCK_COMMAND_ENV_FROM_ENV_JSON":  string(mapping),
		"AGENTDOCK_ACP_MAX_CONCURRENT_PROMPTS": strconv.Itoa(options.ACPMaxPrompts),
		"AGENTDOCK_ACP_INTERACTION_TIMEOUT_MS": strconv.Itoa(options.ACPInteractionMS),
	}, nil
}

func runRuntimeOptionsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("agentdock config "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("runtime-root", "", "桌面运行目录")
	raw := flags.String("options-json", "", "运行配置 JSON；不包含凭据")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*root) == "" {
		return configCommandUsageError()
	}
	if args[0] == "runtime-get" {
		if *raw != "" {
			return errors.New("runtime-get 不接受 options-json")
		}
		view, err := platformReadRuntimeOptions(*root)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(view)
	}
	if len(*raw) > 64<<10 || strings.TrimSpace(*raw) == "" {
		return errors.New("运行配置 JSON 必须非空且不超过 64 KiB")
	}
	var options RuntimeOptions
	if err := json.Unmarshal([]byte(*raw), &options); err != nil {
		return fmt.Errorf("运行配置 JSON 无效: %w", err)
	}
	options, err := normalizeRuntimeOptions(options)
	if err != nil {
		return err
	}
	if err := platformSaveRuntimeOptions(ctx, *root, options); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, `{"updated":true}`)
	return err
}
