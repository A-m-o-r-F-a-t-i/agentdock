package desktopruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const maxServiceLogTailBytes int64 = 8 << 20

// tailServiceLogFile returns a bounded tail and optionally follows the active
// file. The path is selected by platform code from an installed runtime
// manifest; callers cannot use this helper as an arbitrary file reader.
func tailServiceLogFile(ctx context.Context, path string, lines int, follow bool, output io.Writer) error {
	if lines < 1 || lines > 10000 {
		return errors.New("日志行数必须在 1 到 10000 之间")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开 AgentDock 服务日志失败: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取 AgentDock 服务日志状态失败: %w", err)
	}
	start := info.Size() - maxServiceLogTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return fmt.Errorf("定位 AgentDock 服务日志失败: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxServiceLogTailBytes))
	if err != nil {
		return fmt.Errorf("读取 AgentDock 服务日志失败: %w", err)
	}
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	data = lastLogLines(data, lines)
	if len(data) != 0 {
		if _, err := output.Write(data); err != nil {
			return fmt.Errorf("写入 AgentDock 服务日志失败: %w", err)
		}
		if data[len(data)-1] != '\n' {
			if _, err := io.WriteString(output, "\n"); err != nil {
				return fmt.Errorf("写入 AgentDock 服务日志换行失败: %w", err)
			}
		}
	}
	if !follow {
		return nil
	}

	offset := info.Size()
	activeInfo := info
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			current, statErr := os.Stat(path)
			if statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("读取 AgentDock 服务日志状态失败: %w", statErr)
			}
			if !os.SameFile(activeInfo, current) {
				if err := file.Close(); err != nil {
					return err
				}
				file, err = os.Open(path)
				if err != nil {
					return fmt.Errorf("重新打开轮转后的 AgentDock 服务日志失败: %w", err)
				}
				activeInfo = current
				offset = 0
			} else if current.Size() < offset {
				offset = 0
			}
			if current.Size() == offset {
				continue
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				return fmt.Errorf("定位新增服务日志失败: %w", err)
			}
			written, copyErr := io.Copy(output, file)
			offset += written
			if copyErr != nil {
				return fmt.Errorf("读取新增服务日志失败: %w", copyErr)
			}
		}
	}
}

func lastLogLines(data []byte, lines int) []byte {
	trimmed := bytes.TrimRight(data, "\n")
	if len(trimmed) == 0 {
		return nil
	}
	parts := bytes.Split(trimmed, []byte{'\n'})
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return bytes.Join(parts, []byte{'\n'})
}
