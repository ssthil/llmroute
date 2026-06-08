package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/config"
	"github.com/ssthil/llmroute/internal/database"
)

func newKeysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage stored provider API keys",
		Long: `keys inspects and updates the provider API keys stored in
~/.config/llmroute/keys.json (mode 0600). An exported environment variable
always overrides a stored key.`,
	}
	cmd.AddCommand(newKeysListCmd(), newKeysSetCmd(), newKeysRmCmd())
	return cmd
}

func newKeysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List providers and their key status (values masked)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openDB()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			keys, err := config.LoadKeys()
			if err != nil {
				return err
			}
			providers, err := db.AllProviders()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			header(out, "keys")
			for _, p := range providers {
				if !p.NeedsKey {
					fmt.Fprintf(out, "  %s %-10s %s\n", gray(glyphDot), p.Name, gray("local · no key needed"))
					continue
				}
				switch {
				case os.Getenv(p.KeyEnv) != "":
					fmt.Fprintf(out, "  %s %-10s %s\n", green(glyphOK), p.Name,
						gray(fmt.Sprintf("from $%s %s", p.KeyEnv, maskKey(os.Getenv(p.KeyEnv)))))
				case keys.Get(p.Name) != "":
					fmt.Fprintf(out, "  %s %-10s %s\n", green(glyphOK), p.Name,
						gray("stored "+maskKey(keys.Get(p.Name))))
				default:
					fmt.Fprintf(out, "  %s %-10s %s\n", gray(glyphOff), p.Name,
						gray(fmt.Sprintf("not set (set $%s or 'llmroute keys set %s')", p.KeyEnv, p.Name)))
				}
			}
			return nil
		},
	}
}

func newKeysSetCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "set <provider>",
		Short: "Set or update a provider's API key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]

			db, err := openDB()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			prov, err := db.Provider(provider)
			if err != nil {
				return fmt.Errorf("%w (see 'llmroute keys list')", err)
			}
			if !prov.NeedsKey {
				return fmt.Errorf("provider %q is local/no-key — nothing to set", provider)
			}

			var value string
			if fromStdin {
				r := bufio.NewReader(cmd.InOrStdin())
				line, _ := r.ReadString('\n')
				value = strings.TrimSpace(line)
			} else {
				if !isTerminal(cmd.InOrStdin()) {
					return fmt.Errorf("not a terminal; use --stdin to pipe the key")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s API key: ", cyan(glyphArrow), provider)
				value, err = readSecret()
				fmt.Fprintln(cmd.OutOrStdout())
				if err != nil {
					return fmt.Errorf("read key: %w", err)
				}
				value = strings.TrimSpace(value)
			}
			if value == "" {
				return fmt.Errorf("no key provided")
			}

			keys, err := config.LoadKeys()
			if err != nil {
				return err
			}
			keys.Set(provider, value)
			if err := keys.Save(); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			success(out, "stored %s key %s", provider, gray(maskKey(value)))
			if os.Getenv(prov.KeyEnv) != "" {
				warn(out, "$%s is set and overrides this stored key", prov.KeyEnv)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the key from stdin instead of prompting")
	return cmd
}

func newKeysRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <provider>",
		Short: "Remove a stored provider key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			keys, err := config.LoadKeys()
			if err != nil {
				return err
			}
			if keys.Get(provider) == "" {
				return fmt.Errorf("no stored key for %q", provider)
			}
			keys.Set(provider, "")
			if err := keys.Save(); err != nil {
				return err
			}
			success(cmd.OutOrStdout(), "removed stored %s key", provider)
			return nil
		},
	}
}

// openDB opens the database at the default path.
func openDB() (*database.DB, error) {
	path, err := database.DefaultPath()
	if err != nil {
		return nil, err
	}
	return database.Open(path)
}
