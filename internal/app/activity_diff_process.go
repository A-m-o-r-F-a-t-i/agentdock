package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/process"
	"github.com/uvwt/agentdock/internal/workspace"
)

type diffBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
}

func (b *diffBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxActivityDiffBytes - b.buffer.Len()
	if remaining < len(data) {
		b.truncated = true
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(data[:min(remaining, len(data))])
	}
	return len(data), nil
}

func (r *Runtime) runActivityGit(ctx context.Context, record workspace.Record, origin activity.Event, arguments []string) (string, int, bool, error) {
	var command *exec.Cmd
	globalConfig := "/dev/null"
	if record.Runtime == "wsl" {
		args := []string{}
		if record.Distribution != "" {
			args = append(args, "--distribution", record.Distribution)
		}
		args = append(args, "--exec", "env", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "git", "-c", "core.hooksPath=/dev/null")
		args = append(args, arguments...)
		command = exec.CommandContext(ctx, "wsl.exe", args...)
	} else {
		// Some native Windows ARM64 Git builds reject NUL as a configuration
		// file. Use an owned empty regular file and an empty hook directory;
		// never fall back to the user's unrelated global configuration.
		directory, err := os.MkdirTemp("", "agentdock-git-read-")
		if err != nil {
			return "", -1, false, err
		}
		defer os.RemoveAll(directory)
		globalConfig = filepath.Join(directory, "config")
		if err := os.WriteFile(globalConfig, nil, 0600); err != nil {
			return "", -1, false, err
		}
		args := append([]string{"-c", "core.hooksPath=" + directory}, arguments...)
		command = exec.CommandContext(ctx, "git", args...)
		command.Dir = r.ws.Root()
	}
	command.WaitDelay = 2 * time.Second
	secrets := []string{r.cfg.AuthToken, r.cfg.NexusDeviceToken}
	// Git repository/config environment overrides cannot redirect this validated read.
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if !strings.HasPrefix(upper, "GIT_") {
			command.Env = append(command.Env, entry)
		}
		for _, marker := range []string{"TOKEN", "PASSWORD", "PASSWD", "SECRET", "API_KEY", "APIKEY", "PRIVATE_KEY"} {
			if strings.Contains(upper, marker) {
				secrets = append(secrets, value)
				break
			}
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	process.Configure(command)
	stdout, stderr := &diffBuffer{}, &diffBuffer{}
	command.Stdout, command.Stderr = stdout, stderr
	started := time.Now()
	if err := command.Start(); err != nil {
		return "", -1, false, err
	}
	event := activity.Event{Binding: origin.Binding, Kind: "command.started", ToolName: "activity_diff", Status: "running", Runtime: record.Runtime, Workdir: record.Root, DisplayCommand: "git " + strings.Join(arguments, " "), Title: "Read current file diff"}
	r.recordObservedEvent(event, nil)
	err := command.Wait()
	code := command.ProcessState.ExitCode()
	ok := err == nil && code == 0
	event.Kind, event.Status = "command.completed", "success"
	if !ok {
		event.Status = "failed"
	}
	event.ExitCode, event.CommandOK = &code, &ok
	event.ElapsedMS = time.Since(started).Milliseconds()
	event.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	redactor := activity.NewRedactor(secrets...)
	diagnostic := redactor.Text(completeDiffOutput(stderr), 4096)
	event.StderrPreview, event.StderrTruncated = diagnostic, stderr.truncated
	r.recordObservedEvent(event, nil)
	if err != nil {
		err = fmt.Errorf("git read failed (exit %d): %s: %w", code, strings.TrimSpace(diagnostic), err)
	}
	return redactor.Text(completeDiffOutput(stdout), maxActivityDiffBytes), code, stdout.truncated, err
}

func completeDiffOutput(buffer *diffBuffer) string {
	text := buffer.buffer.String()
	if buffer.truncated {
		// A truncated last line might contain a split credential, so omit it entirely.
		if index := strings.LastIndexByte(text, '\n'); index >= 0 {
			text = text[:index+1]
		} else {
			text = ""
		}
	}
	return text
}
