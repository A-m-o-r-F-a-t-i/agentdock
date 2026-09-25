package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/uvwt/agentdock/internal/buildinfo"
)

func printVersion(output io.Writer) {
	printVersionLabel(output, buildinfo.ProductName)
}

func printVersionLabel(output io.Writer, label string) {
	info := buildinfo.Current()
	fmt.Fprintf(output, "%s v%s\n", label, strings.TrimPrefix(info.Version, "v"))
	if label != buildinfo.ProductName {
		fmt.Fprintf(output, "product: %s\n", buildinfo.ProductName)
	}
	fmt.Fprintf(output, "commit: %s\n", info.Commit)
	fmt.Fprintf(output, "built: %s\n", info.BuildDate)
	fmt.Fprintf(output, "go: %s\n", info.GoVersion)
	fmt.Fprintf(output, "platform: %s\n", info.Platform)
}
