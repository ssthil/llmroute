// Package cli wires the Cobra command tree for llmroute.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/database"
	"github.com/ssthil/llmroute/internal/security"
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
		Args:          cobra.NoArgs,
		// With no subcommand, show the model catalog + status instead of help.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showStatus(cmd.OutOrStdout())
		},
	}

	root.AddCommand(newInitCmd())
	root.AddCommand(newProxyCmd())
	root.AddCommand(newStatsCmd())
	root.AddCommand(newKeysCmd())
	root.AddCommand(newModelsCmd())
	return root
}

// printBanner renders the boxed wordmark (title · version · tagline). Shown on
// the first-run welcome and atop the bare-command status view.
func printBanner(out io.Writer) {
	const w = 50
	fmt.Fprintln(out)
	boxTop(out, w)
	boxRow(out, w, "  llmroute "+version, func(s string) string { return bold(cyan(s)) })
	boxRow(out, w, "  one endpoint · many providers · auto-routed", dim)
	boxBottom(out, w)
}

// printWelcome renders the first-run intro: the banner, getting-started steps,
// and quick tips.
func printWelcome(out io.Writer) {
	printBanner(out)
	fmt.Fprintln(out)

	fmt.Fprintln(out, bold("Get started"))
	step(out, 1, "llmroute init", "choose models & add provider keys")
	step(out, 2, "llmroute proxy", "start the gateway on 127.0.0.1:4040")
	step(out, 3, "point your client", "at http://127.0.0.1:4040/v1")
	fmt.Fprintln(out)

	fmt.Fprintln(out, bold("Tips"))
	tip(out, "llmroute", "show your models & their status")
	tip(out, "llmroute models add", "add a local model (Ollama) or another provider")
	tip(out, "llmroute keys set <p>", "update one provider's key")
	tip(out, "llmroute --help", "all commands · set NO_COLOR=1 to disable color")
	fmt.Fprintln(out)
}

// showStatus prints the model catalog with status, or a getting-started hint if
// the database hasn't been created yet (so bare `llmroute` never silently
// provisions state).
func showStatus(out io.Writer) error {
	dir, err := security.ConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, database.DBFileName)
	if _, err := os.Stat(path); err != nil {
		printWelcome(out)
		return nil
	}

	db, err := database.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	models, err := db.AllModels()
	if err != nil {
		return err
	}
	enabled := 0
	for _, m := range models {
		if m.Enabled {
			enabled++
		}
	}
	printBanner(out)
	fmt.Fprintln(out)
	fmt.Fprintln(out, dim(fmt.Sprintf("%d of %d models enabled", enabled, len(models))))
	renderModelTable(out, models)
	fmt.Fprintln(out)
	note(out, "configure: %s · keys: %s · run: %s",
		bold("llmroute init"), bold("llmroute keys list"), bold("llmroute proxy"))
	return nil
}

// Execute builds the command tree and runs it.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
