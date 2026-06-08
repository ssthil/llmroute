package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/config"
	"github.com/ssthil/llmroute/internal/database"
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
			header(out, "setup")
			field(out, "config", dim(dir)+"  "+gray("(0700)"))
			field(out, "database", dim(path)+"  "+gray("(0600)"))
			fmt.Fprintln(out)

			interactive := !nonInteractive && isTerminal(cmd.InOrStdin())
			if !interactive {
				return printCatalog(out, db, "non-interactive setup — all models enabled (keys from env vars)")
			}

			keys, err := config.LoadKeys()
			if err != nil {
				return err
			}
			pio := &promptIO{
				in:     bufio.NewReader(cmd.InOrStdin()),
				out:    out,
				secret: readSecret,
			}
			if err := runWizard(db, keys, pio); err != nil {
				return err
			}
			fmt.Fprintln(out)
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
	providers, err := db.ProvidersMap()
	if err != nil {
		return err
	}
	groups := groupByProvider(models)

	fmt.Fprintln(p.out, dim("Choose which models to enable, then add each provider's API key."))
	fmt.Fprintln(p.out, dim("Press Enter to accept the [default]; keys are hidden as you type."))
	fmt.Fprintln(p.out)

	for _, g := range groups {
		local := ""
		if prov := providers[g.provider]; !prov.NeedsKey {
			local = gray(" (local)")
		}
		fmt.Fprintf(p.out, "%s %s%s\n", cyan(glyphDot), bold(g.provider), local)
		anyEnabled := false
		for _, m := range g.models {
			label := fmt.Sprintf("  enable %s %s", m.Identifier,
				gray(fmt.Sprintf("(%s · cost %.2f)", m.IntentTags, m.CostMultiplier)))
			enable, err := askYesNo(p, label, m.Enabled)
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
			note(p.out, "no %s models enabled — skipping key", g.provider)
			fmt.Fprintln(p.out)
			continue
		}

		prov := providers[g.provider]
		if !prov.NeedsKey {
			note(p.out, "%s is local/no-key — nothing to enter", g.provider)
			fmt.Fprintln(p.out)
			continue
		}
		if err := promptProviderKey(p, keys, prov); err != nil {
			return err
		}
		fmt.Fprintln(p.out)
	}

	if err := addCustomProviders(db, keys, p); err != nil {
		return err
	}

	if err := keys.Save(); err != nil {
		return err
	}
	path, _ := config.KeysPath()
	success(p.out, "saved provider keys to %s %s", path, gray("(0600)"))
	return nil
}

// addCustomProviders lets the user register their own OpenAI-compatible
// providers (Groq, Cerebras, a local Ollama, …) interactively. It loops until
// the user declines to add another.
func addCustomProviders(db *database.DB, keys *config.Keys, p *promptIO) error {
	for {
		fmt.Fprintln(p.out)
		add, err := askYesNo(p, fmt.Sprintf("%s add a custom provider %s",
			cyan(glyphDot), gray("(Groq, Cerebras, local Ollama, …)")), false)
		if err != nil {
			return err
		}
		if !add {
			return nil
		}

		name, err := askLine(p, "provider name", "")
		if err != nil {
			return err
		}
		name = strings.ToLower(name)
		if name == "" {
			warn(p.out, "skipped — no provider name given")
			continue
		}

		baseURL, err := askLine(p, "base URL (…/v1/chat/completions)", "")
		if err != nil {
			return err
		}
		if baseURL == "" {
			warn(p.out, "skipped — no base URL given")
			continue
		}

		needsKey, err := askYesNo(p, "  requires an API key", true)
		if err != nil {
			return err
		}
		keyEnv := ""
		if needsKey {
			keyEnv, err = askLine(p, "key env var", strings.ToUpper(name)+"_API_KEY")
			if err != nil {
				return err
			}
		}

		if err := db.UpsertProvider(database.Provider{
			Name: name, BaseURL: baseURL, KeyEnv: keyEnv, NeedsKey: needsKey,
		}); err != nil {
			return err
		}
		success(p.out, "added provider %s %s", name, gray(baseURL))

		id, err := askLine(p, "model id", "")
		if err != nil {
			return err
		}
		if id == "" {
			warn(p.out, "provider saved, but no model added (use 'llmroute models add' later)")
			continue
		}
		intents, err := askLine(p, "intents (comma: chat,code,vision)", "chat")
		if err != nil {
			return err
		}
		cost := parseCost(mustLine(p, "cost multiplier", "1.0"))

		if err := db.AddModel(database.Model{
			Provider: name, Identifier: id, IntentTags: intents, CostMultiplier: cost,
		}); err != nil {
			warn(p.out, "could not add model: %v", err)
			continue
		}
		success(p.out, "added model %s", id)

		if needsKey {
			if err := promptProviderKey(p, keys, database.Provider{Name: name, KeyEnv: keyEnv, NeedsKey: true}); err != nil {
				return err
			}
		} else {
			note(p.out, "local/no-key provider — no key needed")
		}
	}
}

// askLine prompts for a single line of input, returning def on an empty reply.
func askLine(p *promptIO, prompt, def string) (string, error) {
	hint := ""
	if def != "" {
		hint = dim(" [" + def + "]")
	}
	fmt.Fprintf(p.out, "  %s %s%s: ", cyan(glyphArrow), prompt, hint)
	line, err := p.in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func mustLine(p *promptIO, prompt, def string) string {
	v, err := askLine(p, prompt, def)
	if err != nil {
		return def
	}
	return v
}

func parseCost(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 1.0
	}
	return v
}

// promptProviderKey asks for (or keeps) a provider's API key.
func promptProviderKey(p *promptIO, keys *config.Keys, prov database.Provider) error {
	provider := prov.Name
	env := prov.KeyEnv
	if env == "" {
		env = strings.ToUpper(provider) + "_API_KEY"
	}
	existing := keys.Get(provider)

	hint := ""
	if existing != "" {
		hint = " [Enter to keep existing]"
	} else if v := os.Getenv(env); v != "" {
		hint = fmt.Sprintf(" [%s is set in env; Enter to skip]", env)
	}

	fmt.Fprintf(p.out, "  %s %s%s: ", cyan(glyphArrow), provider+" API key", dim(hint))
	val, err := p.secret()
	if err != nil {
		return fmt.Errorf("read %s key: %w", provider, err)
	}
	fmt.Fprintln(p.out) // ReadPassword leaves the cursor on the same line
	val = strings.TrimSpace(val)

	switch {
	case val != "":
		keys.Set(provider, val)
		success(p.out, "stored %s key %s", provider, gray(maskKey(val)))
	case existing != "":
		success(p.out, "kept existing %s key %s", provider, gray(maskKey(existing)))
	default:
		warn(p.out, "no %s key stored (set %s to use it)", provider, env)
	}
	return nil
}

// askYesNo reads a y/n answer, returning def on an empty line.
func askYesNo(p *promptIO, question string, def bool) (bool, error) {
	suffix := dim(" [y/N] ")
	if def {
		suffix = dim(" [Y/n] ")
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

// printCatalog prints the current model catalog with enabled state.
func printCatalog(out io.Writer, db *database.DB, summary string) error {
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
	info(out, "%s", summary)
	fmt.Fprintf(out, "%s\n", dim(fmt.Sprintf("%d of %d models enabled", enabled, len(models))))
	renderModelTable(out, models)
	return nil
}

// renderModelTable prints models as an aligned, glyph-marked list. Width
// padding is applied to the plain text *before* colorizing, so ANSI escape
// bytes never count toward column width.
func renderModelTable(out io.Writer, models []database.Model) {
	for _, m := range models {
		mark := gray(glyphOff)
		name := fmt.Sprintf("%-24s", m.Identifier)
		if m.Enabled {
			mark = green(glyphOK)
		} else {
			name = dim(name)
		}
		provider := gray(fmt.Sprintf("%-10s", m.Provider))
		meta := gray(fmt.Sprintf("cost %.2f · %s", m.CostMultiplier, m.IntentTags))
		fmt.Fprintf(out, "  %s %s %s %s\n", mark, name, provider, meta)
	}
}
