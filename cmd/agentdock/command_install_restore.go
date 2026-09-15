package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/uvwt/agentdock/internal/installer"
)

func runInstallRestoreFiles(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("agentdock install restore-files", flag.ContinueOnError)
	flags.SetOutput(stderr)
	request := installer.Request{Action: installer.ActionRestoreFiles}
	flags.StringVar(&request.InstallRoot, "install-root", "", "安装根目录")
	flags.StringVar(&request.RuntimeRoot, "runtime-root", "", "运行配置目录")
	flags.StringVar(&request.TransactionID, "transaction-id", "", "本次事务 ID，必填")
	flags.StringVar(&request.PayloadDir, "payload-dir", "", "已验证的新载荷，用于停止运行时")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(request.TransactionID) == "" {
		return errors.New("restore-files requires --install-root and --transaction-id")
	}
	result, err := (installer.Engine{}).Run(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
