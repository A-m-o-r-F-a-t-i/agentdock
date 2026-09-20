//go:build windows

package desktopruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func findTailscaleBinary(saved string) (string, error) {
	return discoverTailscaleBinary(saved, exec.LookPath, os.Getenv("ProgramFiles"))
}

func discoverTailscaleBinary(saved string, lookPath func(string) (string, error), programFiles string) (string, error) {
	if saved = strings.TrimSpace(saved); saved != "" {
		if !filepath.IsAbs(saved) {
			return "", tailscaleProblem("invalid_binary", "Tailscale 客户端路径必须是绝对路径")
		}
		if info, err := os.Stat(saved); err == nil && info.Mode().IsRegular() {
			return filepath.Clean(saved), nil
		} else if err == nil {
			return "", tailscaleProblem("invalid_binary", "Tailscale 客户端路径不是普通文件")
		}
	}
	if candidate, err := lookPath("tailscale.exe"); err == nil && filepath.IsAbs(candidate) {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return filepath.Clean(candidate), nil
		}
	}
	if programFiles != "" {
		candidate := filepath.Join(programFiles, "Tailscale", "tailscale.exe")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", tailscaleProblem("not_installed", "未检测到 Tailscale 客户端，请先安装并登录官方客户端")
}

type tailscaleOutputBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (output *tailscaleOutputBuffer) Write(data []byte) (int, error) {
	length := len(data)
	remaining := output.limit - output.buffer.Len()
	if len(data) > remaining {
		output.overflow = true
		data = data[:remaining]
	}
	_, _ = output.buffer.Write(data)
	// Continue draining the child without allocating additional memory.
	return length, nil
}

func allowedTailscaleCommand(arguments []string) bool {
	if len(arguments) == 3 && arguments[0] == "status" && arguments[1] == "--json" && arguments[2] == "--peers=false" {
		return true
	}
	if len(arguments) == 3 && arguments[0] == "funnel" && arguments[1] == "status" && arguments[2] == "--json" {
		return true
	}
	if len(arguments) != 6 || (arguments[0] != "funnel" && arguments[0] != "serve") || arguments[1] != "--bg" || arguments[2] != "--yes" || arguments[3] != "--https=443" || (arguments[4] != "--set-path=/" && arguments[4] != "--set-path=/mcp") {
		return false
	}
	if arguments[5] == "off" {
		return true
	}
	if arguments[4] == "--set-path=/" {
		return validTailscaleLocalOrigin(arguments[5])
	}
	return strings.HasSuffix(arguments[5], "/mcp") && validTailscaleLocalOrigin(strings.TrimSuffix(arguments[5], "/mcp"))
}

func newWindowsTailscaleClient(binary string) tailscaleClient {
	return tailscaleClient{run: func(parent context.Context, timeout time.Duration, arguments ...string) ([]byte, error) {
		if !allowedTailscaleCommand(arguments) {
			return nil, errors.New("refusing an unsupported Tailscale CLI operation")
		}
		if timeout <= 0 || timeout > tailscaleMutationTimeout {
			return nil, errors.New("Tailscale command requires a bounded timeout")
		}
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		command := exec.CommandContext(ctx, binary, arguments...)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		command.WaitDelay = 2 * time.Second
		stdout := &tailscaleOutputBuffer{limit: tailscaleJSONLimit}
		stderr := &tailscaleOutputBuffer{limit: 16 * 1024}
		command.Stdout, command.Stderr = stdout, stderr
		err := command.Run()
		if ctx.Err() != nil {
			return nil, errors.Join(tailscaleProblem("command_timeout", "Tailscale 命令超时或已取消，请检查客户端状态"), ctx.Err())
		}
		if stdout.overflow || stderr.overflow {
			return nil, tailscaleProblem("output_limit", "Tailscale 命令输出超过上限，未继续执行")
		}
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil, tailscaleProblem("command_failed", fmt.Sprintf("Tailscale 命令失败（退出码 %d），请在官方客户端检查连接和授权", exit.ExitCode()))
			}
			// Do not echo child output or environment: either may contain account data.
			return nil, tailscaleProblem("command_failed", "无法运行 Tailscale 客户端，请检查文件和访问权限")
		}
		return append([]byte(nil), stdout.buffer.Bytes()...), nil
	}}
}
