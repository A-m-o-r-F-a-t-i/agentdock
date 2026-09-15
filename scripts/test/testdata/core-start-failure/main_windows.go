//go:build windows

// Test-only payload wrapper: normal Engine/version commands are delegated;
// the installed trial generation deliberately fails during Core startup.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "service" && os.Args[2] == "launch-core" {
		fmt.Fprintln(os.Stderr, "isolated Core startup fault before installer JSON acknowledgement")
		os.Exit(23)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	command := exec.Command(filepath.Join(filepath.Dir(executable), "agentdock-real.exe"), os.Args[1:]...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
