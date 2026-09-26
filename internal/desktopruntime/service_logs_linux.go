//go:build linux

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

func platformServiceLogs(ctx context.Context, runtimeRoot string, lines int, follow bool, stdout, stderr io.Writer) error {
	manifest, _, err := loadUnixRuntime(runtimeRoot)
	if err != nil {
		return err
	}
	switch linuxServiceManager(manifest) {
	case "systemd":
		args := []string{"--no-pager", "--unit", manifest.ServiceName, "--lines", strconv.Itoa(lines), "--output", "short-iso"}
		if follow {
			args = append(args, "--follow")
		}
		command := exec.CommandContext(ctx, "journalctl", args...)
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}
			return fmt.Errorf("读取 systemd 服务日志失败: %w", err)
		}
		return nil
	case "openrc":
		path, err := openRCLogPath(manifest.ServiceName, "agentdock.err.log")
		if err != nil {
			return err
		}
		return tailServiceLogFile(ctx, path, lines, follow, stdout)
	default:
		return errors.New("未检测到 systemd 或 OpenRC，无法读取服务日志")
	}
}
