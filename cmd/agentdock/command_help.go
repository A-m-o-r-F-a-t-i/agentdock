package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const agentdockCommandGroups = "status workspace task conversation call activity insertion approval permission skill plugin config service doctor logs completion version update"

func runHelpCommand(args []string, output io.Writer) error {
	if len(args) > 1 {
		return errors.New("用法：agentdock help [命令]")
	}
	command := ""
	if len(args) == 1 {
		command = strings.TrimSpace(args[0])
	}
	if command != "" {
		_, err := fmt.Fprintf(output, "AgentDock Workbench 命令：%s\n\n运行 `agentdock %s --help` 查看该命令的参数；完整矩阵见 docs/cli/command-matrix.md。\n", command, command)
		return err
	}
	_, err := fmt.Fprint(output, `AgentDock Workbench CLI

用法：
  agentdock <命令> [参数]

在线控制：
  status  workspace  task  conversation  call  activity
  insertion  approval  permission  skill  plugin  doctor

本机管理：
  config  service  logs  completion  version  update

通用在线选项：
  --endpoint URL       Core 地址
  --token-file PATH    Bearer/OAuth token 文件
  --profile NAME       本地 CLI Profile
  --format FORMAT      human、json 或 jsonl
  --timeout DURATION   普通请求超时
  --workspace ID       默认工作区
  --task ID            默认任务
  --conversation ID    默认会话
  --call ID            默认调用
  --quiet              成功时不输出
  --verbose            输出脱敏请求元数据

安全约束：
  token 不接受命令行明文值；管理写入仍要求 Core 的认证和直连回环校验。
  永久删除、完全权限和乐观并发更新需要显式确认参数。

其他：
  agentdock help <命令>
  agentdock completion <bash|zsh|fish>
`)
	return err
}

func runCompletionCommand(args []string, output io.Writer) error {
	if len(args) != 1 {
		return errors.New("用法：agentdock completion <bash|zsh|fish>")
	}
	commands := agentdockCommandGroups
	switch strings.ToLower(args[0]) {
	case "bash":
		_, err := fmt.Fprintf(output, `_agentdock_completion() {
  local cur
  cur="${COMP_WORDS[COMP_CWORD]}"
  COMPREPLY=( $(compgen -W %q -- "$cur") )
}
complete -F _agentdock_completion agentdock
`, commands)
		return err
	case "zsh":
		_, err := fmt.Fprintf(output, `#compdef agentdock
_agentdock() {
  local -a commands
  commands=(%s)
  _describe 'command' commands
}
compdef _agentdock agentdock
`, strings.Join(strings.Fields(commands), " "))
		return err
	case "fish":
		for _, command := range strings.Fields(commands) {
			if _, err := fmt.Fprintf(output, "complete -c agentdock -f -n '__fish_use_subcommand' -a %s\n", command); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("completion 仅支持 bash、zsh 或 fish")
	}
}

func runLogsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：agentdock logs <tail|follow|export> --runtime-root <目录> ...")
	}
	action := strings.ToLower(args[0])
	switch action {
	case "tail":
		return runServiceCommand(ctx, append([]string{"logs"}, args[1:]...), stdout, stderr)
	case "follow":
		forwarded := append([]string{"logs", "--follow"}, args[1:]...)
		return runServiceCommand(ctx, forwarded, stdout, stderr)
	case "export":
		flags := flag.NewFlagSet("agentdock logs export", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runtimeRoot := flags.String("runtime-root", "", "AgentDock 桌面运行目录")
		lines := flags.Int("lines", 1000, "导出最近日志行数（1-10000）")
		outputPath := flags.String("output", "", "导出文件")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*runtimeRoot) == "" || strings.TrimSpace(*outputPath) == "" || *lines < 1 || *lines > 10000 {
			return errors.New("用法：agentdock logs export --runtime-root <目录> --output <文件> [--lines 1-10000]")
		}
		var data bytes.Buffer
		if err := runServiceCommand(ctx, []string{"logs", "--runtime-root", *runtimeRoot, "--lines", strconv.Itoa(*lines)}, &data, stderr); err != nil {
			return err
		}
		if err := os.WriteFile(*outputPath, data.Bytes(), 0o600); err != nil {
			return fmt.Errorf("写入日志导出失败: %w", err)
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"ok": true, "output": *outputPath, "bytes": data.Len()})
	default:
		return errors.New("logs 仅支持 tail、follow 或 export")
	}
}
