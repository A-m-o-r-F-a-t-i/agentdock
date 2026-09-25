//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/uvwt/agentdock/internal/desktopruntime"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/updateengine"
)

// runTaskCoreHost 是 elevated Core 模式下长期运行的计划任务入口。
// 由稳定 GUI shim 直接拥有 generation Core，让 Task Scheduler 只绑定稳定进程边界；
// 安装、修复、更新和回滚期间都不会长期占用可替换的 versioned WPF 可执行文件。
func runTaskCoreHost(args []string) (int, error) {
	flags := flag.NewFlagSet("task-core-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runtimeRootFlag := flags.String("runtime-root", "", "AgentDock runtime root")
	if err := flags.Parse(args); err != nil {
		return 1, err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*runtimeRootFlag) == "" {
		return 1, errors.New("task core host requires --runtime-root")
	}

	executable, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("resolve AgentDock task host entry: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return 1, fmt.Errorf("resolve AgentDock task host entry path: %w", err)
	}
	if !strings.EqualFold(filepath.Base(executable), updateengine.StableTrayShimName) {
		return 1, fmt.Errorf("task core host requires the stable GUI entry %s", updateengine.StableTrayShimName)
	}

	runtimeRoot, err := filepath.Abs(strings.TrimSpace(*runtimeRootFlag))
	if err != nil {
		return 1, fmt.Errorf("resolve task core host runtime root: %w", err)
	}
	stableRoot := filepath.Dir(filepath.Dir(executable))
	if !sameWindowsPath(runtimeRoot, stableRoot) {
		return 1, fmt.Errorf("task core host runtime root %s does not match stable entry root %s", runtimeRoot, stableRoot)
	}

	if _, _, err := desktopruntime.ValidateManagedRuntimeTask(context.Background(), runtimeRoot); err != nil {
		return 1, err
	}
	store, err := updateengine.NewStore(runtimeRoot)
	if err != nil {
		return 1, err
	}
	layout, err := updateengine.NewWindowsLayout(runtimeRoot)
	if err != nil {
		return 1, err
	}
	active, err := resolveActiveWithRecovery(runtimeRoot, store, layout)
	if err != nil {
		return 1, err
	}
	coreBinary := layout.GenerationCore(active.ActiveVersion)
	if info, err := os.Stat(coreBinary); err != nil || info.IsDir() {
		if err == nil {
			err = errors.New("path is a directory")
		}
		return 1, fmt.Errorf("resolve active AgentDock Core %s: %w", coreBinary, err)
	}

	compatibilityCtx, compatibilityCancel := context.WithTimeout(context.Background(), 6*time.Second)
	compatibilityErr := desktopruntime.CheckExecutionCompatibility(compatibilityCtx, runtimeRoot, coreBinary)
	compatibilityCancel()
	if compatibilityErr != nil {
		return 1, compatibilityErr
	}
	if err := desktopruntime.RunTaskRuntime(context.Background(), runtimeRoot, coreBinary, active.ActiveVersion); err != nil {
		return 1, err
	}
	return 0, nil
}
