package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ssthil/llmroute/internal/config"
	"github.com/ssthil/llmroute/internal/database"
	"github.com/ssthil/llmroute/internal/router"
	"github.com/ssthil/llmroute/internal/security"
)

func newInitCmd() *cobra.Command {
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up the config directory, choose models, and add provider keys",
		Long: `init creates ~/.config/llmroute with 0700 permissions, provisions the
records.db SQLite file with 0600 permissions, runs migrations, and seeds the
baseline model catalog.

Run interactively (the default on a terminal) it walks the model catalog so you
can choose which models to enable and enter each provider's API key. Keys are
stored in a 0600 keys.json inside the config dir. An exported environment
variable (e.g. OPENAI_API_KEY) always overrides a stored key.

Use --yes for non-interactive setup: every model is enabled and no keys are
prompted (supply them via environment variables).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := security.EnsureConfigDir()
			if err != nil {
				return err
			}
			path, err := database.DefaultPath()
			if err != nil {
				return err
			}
			db, err := database.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "config dir : %s (0700)\n", dir)
			fmt.Fprintf(out, "database   : %s (0600)\n\n", path)

			interactive := !nonInteractive && isTerminal(cmd.InOrStdin())
			if !interactive {
				return printCatalog(out, db, "non-interactive setup: all models enabled (keys from env vars)")
			}

			keys, err := config.LoadKeys()
			if err != nil {
				return err
			}
			pio := &promptIO{
				in:  bufio.NewReader(cmd.InOrStdin()),
				out: out,
				secret: func() (string, error) {
					b, err := term.ReadPassword(int(os.Stdin.Fd()))
					return string(b), err
				},
			}
			if err := runWizard(db, keys, pio); err != nil {
				return err
			}
			return printCatalog(out, db, "setup complete")
		},
	}

	cmd.Flags().BoolVarP(&nonInteractive, "yes", "y", false,
		"non-interactive: enable all models and skip key prompts")
	return cmd
}

// promptIO carries the wizard's I/O so the flow can be unit tested with
// injected readers instead of a live terminal.
type promptIO struct {
	in     *bufio.Reader
	out    io.Writer
	secret func() (string, error) // reads one line without echoing
}

// runWizard walks the catalog grouped by provider: each model is toggled
// on/off, and providers with at least one enabled model are prompted for a key.
func runWizard(db *database.DB, keys *config.Keys, p *promptIO) error {
	models, err := db.AllModels()
	if err != nil {
		return err
	}
	groups := groupByProvider(models)

	fmt.Fprintln(p.out, "Choose which models to enable, then add each provider's API key.")
	fmt.Fprintln(p.out, "(press Enter to accept the [default]; keys are hidden as you type)")
	fmt.Fprintln(p.out)

	for _, g := range groups {
		fmt.Fprintf(p.out, "%s\n", strings.ToUpper(g.provider))
		anyEnabled := false
		for _, m := range g.models {
			enable, err := askYesNo(p, fmt.Sprintf("  enable %s (intents: %s, cost %.2f)?",
				m.Identifier, m.IntentTags, m.CostMultiplier), m.Enabled)
			if err != nil {
				return err
			}
			if err := db.SetModelEnabled(m.Identifier, enable); err != nil {
				return err
			}
			if enable {
				anyEnabled = true
			}
		}

		if !anyEnabled {
			fmt.Fprintf(p.out, "  (no %s models enabled — skipping key)\n\n", g.provider)
			continue
		}

		if err := promptProviderKey(p, keys, g.provider); err != nil {
			return err
		}
		fmt.Fprintln(p.out)
	}

	if err := keys.Save(); err != nil {
		return err
	}
	path, _ := config.KeysPath()
	fmt.Fprintf(p.out, "saved provider keys to %s (0600)\n\n", path)
	return nil
}

// promptProviderKey asks for (or keeps) a provider's API key.
func promptProviderKey(p *promptIO, keys *config.Keys, provider string) error {
	env := providerKeyEnv(provider)
	existing := keys.Get(provider)

	hint := ""
	if existing != "" {
		hint = " [Enter to keep existing]"
	} else if v := os.Getenv(env); v != "" {
		hint = fmt.Sprintf(" [%s is set in env; Enter to skip]", env)
	}

	fmt.Fprintf(p.out, "  %s API key%s: ", provider, hint)
	val, err := p.secret()
	if err != nil {
		return fmt.Errorf("read %s key: %w", provider, err)
	}
	fmt.Fprintln(p.out) // ReadPassword leaves the cursor on the same line
	val = strings.TrimSpace(val)

	switch {
	case val != "":
		keys.Set(provider, val)
		fmt.Fprintf(p.out, "  stored %s key\n", provider)
	case existing != "":
		fmt.Fprintf(p.out, "  kept existing %s key\n", provider)
	default:
		fmt.Fprintf(p.out, "  no %s key stored (set %s to use it)\n", provider, env)
	}
	return nil
}

// askYesNo reads a y/n answer, returning def on an empty line.
func askYesNo(p *promptIO, question string, def bool) (bool, error) {
	suffix := " [y/N]: "
	if def {
		suffix = " [Y/n]: "
	}
	fmt.Fprint(p.out, question+suffix)

	line, err := p.in.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		// Treat anything unexpected as the default to keep the flow moving.
		return def, nil
	}
}

type providerGroup struct {
	provider string
	models   []database.Model
}

// groupByProvider buckets models by provider, ordering providers alphabetically
// and models cheapest-first within each.
func groupByProvider(models []database.Model) []providerGroup {
	byProv := map[string][]database.Model{}
	for _, m := range models {
		byProv[m.Provider] = append(byProv[m.Provider], m)
	}
	names := make([]string, 0, len(byProv))
	for name := range byProv {
		names = append(names, name)
	}
	sort.Strings(names)

	groups := make([]providerGroup, 0, len(names))
	for _, name := range names {
		ms := byProv[name]
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].CostMultiplier < ms[j].CostMultiplier })
		groups = append(groups, providerGroup{provider: name, models: ms})
	}
	return groups
}

func providerKeyEnv(provider string) string {
	if p, ok := router.Providers[provider]; ok {
		return p.KeyEnv
	}
	return strings.ToUpper(provider) + "_API_KEY"
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// printCatalog prints the current model catalog with enabled state.
func printCatalog(out io.Writer, db *database.DB, note string) error {
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
	fmt.Fprintf(out, "%s — %d/%d models enabled:\n", note, enabled, len(models))
	for _, m := range models {
		mark := "x"
		if m.Enabled {
			mark = "✓"
		}
		fmt.Fprintf(out, "  [%s] %-22s provider=%-10s cost=%.2f intents=[%s]\n",
			mark, m.Identifier, m.Provider, m.CostMultiplier, m.IntentTags)
	}
	return nil
}
