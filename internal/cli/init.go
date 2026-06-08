package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
				if err := printCatalog(out, db, "non-interactive setup — all models enabled (keys from env vars)"); err != nil {
					return err
				}
				printNextSteps(out)
				return nil
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
			if err := printCatalog(out, db, "setup complete"); err != nil {
				return err
			}
			printNextSteps(out)
			return nil
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

// access modes for the opening prompt.
const (
	accessAPI   = "api"
	accessLocal = "local"
	accessBoth  = "both"
)

// runWizard asks how the user accesses models, then runs the matching setup:
// API keys (cloud catalog), local models (Ollama/LM Studio), or both.
func runWizard(db *database.DB, keys *config.Keys, p *promptIO) error {
	mode, err := askAccessMode(p)
	if err != nil {
		return err
	}

	switch mode {
	case accessLocal:
		// Start clean so only the local models the user adds are enabled.
		if err := disableAllModels(db); err != nil {
			return err
		}
	default: // api or both
		if err := selectCloudModels(db, keys, p); err != nil {
			return err
		}
	}

	if mode == accessLocal || mode == accessBoth {
		if err := setupLocalModels(db, p); err != nil {
			return err
		}
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

// askAccessMode asks whether the user has provider API keys, runs local models,
// or both — so subscription-only users are guided to local models (the legit,
// no-key path) instead of expecting a Claude Pro / ChatGPT Plus login to work.
func askAccessMode(p *promptIO) (string, error) {
	fmt.Fprintln(p.out, bold("How will you access models?"))
	fmt.Fprintf(p.out, "  %s %s %s\n", cyan("1."), bold(fmt.Sprintf("%-18s", "API keys")), gray("OpenAI, Gemini, Anthropic, Mistral, … (usage-based API plans)"))
	fmt.Fprintf(p.out, "  %s %s %s\n", cyan("2."), bold(fmt.Sprintf("%-18s", "Local models")), gray("Ollama / LM Studio — free, no key, runs on your machine"))
	fmt.Fprintf(p.out, "  %s %s %s\n", cyan("3."), bold(fmt.Sprintf("%-18s", "Both")), gray("cloud API keys and local models"))
	note(p.out, "chat subscriptions (Claude Pro, ChatGPT Plus) don't include API access — pick 2 for those")
	fmt.Fprintln(p.out)

	ans, err := askLine(p, "choose", "1")
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(ans) {
	case "2", "local":
		return accessLocal, nil
	case "3", "both":
		return accessBoth, nil
	default:
		return accessAPI, nil
	}
}

// disableAllModels turns off every catalog model (used for a local-only setup).
func disableAllModels(db *database.DB) error {
	models, err := db.AllModels()
	if err != nil {
		return err
	}
	for _, m := range models {
		if err := db.SetModelEnabled(m.Identifier, false); err != nil {
			return err
		}
	}
	return nil
}

// selectCloudModels lists the catalog, applies a multi-select, then prompts for
// the API key of each enabled key-requiring provider.
func selectCloudModels(db *database.DB, keys *config.Keys, p *promptIO) error {
	models, err := db.AllModels()
	if err != nil {
		return err
	}
	providers, err := db.ProvidersMap()
	if err != nil {
		return err
	}

	fmt.Fprintln(p.out)
	fmt.Fprintln(p.out, dim("Pick the models to enable. Keys are entered next (hidden)."))
	fmt.Fprintln(p.out)
	for i, m := range models {
		mark := gray(glyphOff)
		if m.Enabled {
			mark = green(glyphOK)
		}
		local := ""
		if prov := providers[m.Provider]; !prov.NeedsKey {
			local = gray(" · local")
		}
		fmt.Fprintf(p.out, "  %s %s %s %s\n",
			gray(fmt.Sprintf("%2d.", i+1)), mark,
			fmt.Sprintf("%-24s", m.Identifier),
			gray(fmt.Sprintf("%-10s cost %.2f · %s%s", m.Provider, m.CostMultiplier, m.IntentTags, local)))
	}
	fmt.Fprintln(p.out)

	sel, err := askLine(p, "enable which? "+dim("[a]ll / [n]one / numbers e.g. 1,3,5 / Enter=keep"), "")
	if err != nil {
		return err
	}
	mode, set := parseSelection(sel, len(models))
	for i, m := range models {
		var enabled bool
		switch mode {
		case selKeep:
			enabled = m.Enabled
		case selAll:
			enabled = true
		case selNone:
			enabled = false
		case selSubset:
			enabled = set[i+1]
		}
		if err := db.SetModelEnabled(m.Identifier, enabled); err != nil {
			return err
		}
	}

	// Prompt for keys of providers backing the now-enabled models.
	enabled, err := db.EnabledModels()
	if err != nil {
		return err
	}
	fmt.Fprintln(p.out)
	seen := map[string]bool{}
	for _, m := range enabled {
		if seen[m.Provider] {
			continue
		}
		seen[m.Provider] = true
		prov := providers[m.Provider]
		if !prov.NeedsKey {
			continue
		}
		if err := promptProviderKey(p, keys, prov); err != nil {
			return err
		}
	}
	return nil
}

// selection modes for parseSelection.
const (
	selKeep   = "keep"
	selAll    = "all"
	selNone   = "none"
	selSubset = "subset"
)

// parseSelection interprets a multi-select reply against an n-item list.
// "" keeps current, "a"/"all" selects all, "n"/"none" clears, and a
// comma-separated list selects those 1-based indices (out-of-range ignored).
func parseSelection(input string, n int) (string, map[int]bool) {
	s := strings.ToLower(strings.TrimSpace(input))
	switch s {
	case "":
		return selKeep, nil
	case "a", "all":
		return selAll, nil
	case "n", "none":
		return selNone, nil
	}
	set := map[int]bool{}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if i, err := strconv.Atoi(tok); err == nil && i >= 1 && i <= n {
			set[i] = true
		}
	}
	return selSubset, set
}

// localModelLister returns installed model ids from an OpenAI-compatible
// /models endpoint derived from a chat-completions URL. Overridable in tests.
var localModelLister = httpListLocalModels

func httpListLocalModels(chatURL string) ([]string, error) {
	modelsURL := strings.Replace(chatURL, "/chat/completions", "/models", 1)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(modelsURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// localProviderName picks a friendly provider name from a local base URL.
func localProviderName(u string) string {
	switch {
	case strings.Contains(u, ":11434"):
		return "ollama"
	case strings.Contains(u, ":1234"):
		return "lmstudio"
	default:
		return "local"
	}
}

// setupLocalModels guides adding local, no-key models. It tries to auto-detect
// installed models from the server's /models endpoint and falls back to a
// manual model-id entry.
func setupLocalModels(db *database.DB, p *promptIO) error {
	fmt.Fprintln(p.out)
	fmt.Fprintf(p.out, "%s %s\n", cyan(glyphDot), bold("local models"))
	note(p.out, "free, no API key — runs on your machine (Ollama :11434, LM Studio :1234)")

	base, err := askLine(p, "local server URL", "http://127.0.0.1:11434/v1/chat/completions")
	if err != nil {
		return err
	}
	provider := localProviderName(base)

	var chosen []string
	if ids, derr := localModelLister(base); derr == nil && len(ids) > 0 {
		success(p.out, "detected %d model(s) at %s", len(ids), provider)
		for i, id := range ids {
			fmt.Fprintf(p.out, "  %s %s\n", gray(fmt.Sprintf("%2d.", i+1)), id)
		}
		sel, err := askLine(p, "add which? "+dim("[a]ll / numbers e.g. 1,2 / Enter=none"), "")
		if err != nil {
			return err
		}
		mode, set := parseSelection(sel, len(ids))
		for i, id := range ids {
			if mode == selAll || (mode == selSubset && set[i+1]) {
				chosen = append(chosen, id)
			}
		}
	} else {
		warn(p.out, "couldn't detect models at %s", base)
		id, err := askLine(p, "model id (e.g. gemma3:27b), or Enter to skip", "")
		if err != nil {
			return err
		}
		if id != "" {
			chosen = append(chosen, id)
		}
	}

	if len(chosen) == 0 {
		note(p.out, "no local models added")
		return nil
	}

	if err := db.UpsertProvider(database.Provider{Name: provider, BaseURL: base, NeedsKey: false}); err != nil {
		return err
	}
	for _, id := range chosen {
		if err := db.AddModel(database.Model{
			Provider: provider, Identifier: id, IntentTags: "chat,code", CostMultiplier: 0.05,
		}); err != nil {
			warn(p.out, "skip %s: %v", id, err)
			continue
		}
		success(p.out, "added %s %s", provider, id)
	}
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

// printNextSteps prints a short, friendly footer after setup completes.
func printNextSteps(out io.Writer) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, bold("Next"))
	step(out, 1, "llmroute proxy", "start the gateway (127.0.0.1:4040)")
	step(out, 2, "point your client", "at http://127.0.0.1:4040/v1")
	note(out, "review anytime with %s · add models with %s",
		bold("llmroute"), bold("llmroute models add"))
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
