// Package cli wires the Cobra command tree for llmroute.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// SetVersion lets the build inject the release version.
func SetVersion(v string) {
	if v != "" {
		version = v
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "llmroute",
		Short: "A native, multi-LLM routing proxy",
		Long: `llmroute is a standalone loopback proxy that classifies inbound
OpenAI-style chat requests by intent and transparently routes them to the
cheapest capable upstream provider, failing over when a provider is rate
limited or down.

All state lives in a 0600 SQLite database under ~/.config/llmroute.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newInitCmd())
	root.AddCommand(newProxyCmd())
	root.AddCommand(newStatsCmd())
	return root
}

// Execute builds the command tree and runs it.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
