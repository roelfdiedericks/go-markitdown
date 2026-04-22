package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridable via -ldflags "-X main.version=v1.2.3" at build
// time. When left at "dev", we look up the module build info.
var version = "dev"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and exit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("go-markitdown %s\n", resolvedVersion())
			return nil
		},
	}
}

func resolvedVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
