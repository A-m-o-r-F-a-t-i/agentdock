package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/uvwt/agentdock/internal/installer"
	"github.com/uvwt/agentdock/internal/plugin"
)

func runPluginCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：agentdock plugin validate --source <目录或ZIP> | migrate --home <离线数据目录> | list --home <数据目录>")
	}
	flags := flag.NewFlagSet("agentdock plugin "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "标准插件目录或ZIP")
	home := flags.String("home", "", "显式数据目录；migrate 前应停止该实例")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("plugin 子命令不接受额外位置参数")
	}
	switch args[0] {
	case "migrate":
		if *source != "" || !filepath.IsAbs(*home) {
			return errors.New("migrate 需要 --home 绝对目录，且不接受 --source")
		}
		names, err := installer.MigratePluginHome(ctx, *home)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"migrated": names, "format": "Agent Plugins 1.0.0"})
	case "validate":
		if *source == "" || *home != "" {
			return errors.New("validate 需要 --source，且不接受 --home")
		}
		temporary, err := os.MkdirTemp("", "agentdock-plugin-validation-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		store, err := plugin.New(temporary)
		if err != nil {
			return err
		}
		definition, err := store.Validate(*source)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(stdout).Encode(definition); err != nil {
			return err
		}
		if len(definition.Diagnostics) > 0 {
			return fmt.Errorf("插件包含 %d 项校验提示", len(definition.Diagnostics))
		}
		return nil
	case "list":
		if !filepath.IsAbs(*home) || *source != "" {
			return errors.New("list 需要 --home 绝对目录，且不接受 --source")
		}
		store, err := plugin.New(*home)
		if err != nil {
			return err
		}
		items, err := store.List()
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(items)
	default:
		return errors.New("未知 plugin 子命令")
	}
}
