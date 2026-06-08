package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/config"
	"github.com/ssthil/llmroute/internal/database"
)

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List and manage the routable model catalog",
		Long: `models inspects the catalog and lets you add custom or local models
(e.g. an Ollama model on localhost) and enable/disable or remove entries.`,
	}
	cmd.AddCommand(newModelsListCmd(), newModelsAddCmd(), newModelsRmCmd(),
		newModelsEnableCmd(true), newModelsEnableCmd(false))
	return cmd
}

func newModelsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the model catalog and provider endpoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openDB()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			out := cmd.OutOrStdout()
			header(out, "models")
			models, err := db.AllModels()
			if err != nil {
				return err
			}
			renderModelTable(out, models)

			providers, err := db.AllProviders()
			if err != nil {
				return err
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, dim("providers"))
			for _, p := range providers {
				keyInfo := "no key"
				if p.NeedsKey {
					keyInfo = "key $" + p.KeyEnv
				}
				fmt.Fprintf(out, "  %s %-10s %s  %s\n", cyan(glyphDot), p.Name,
					gray(p.BaseURL), gray(keyInfo))
			}
			return nil
		},
	}
}

func newModelsAddCmd() *cobra.Command {
	var provider, baseURL, id, intents, keyEnv string
	var cost float64
	var noKey bool

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a custom or local model (and its provider if new)",
		Long: `add registers a model in the catalog. If its provider does not exist
yet, --base-url is required to create it.

Example — an Ollama model running locally:
  llmroute models add --provider ollama \
    --base-url http://localhost:11434/v1/chat/completions \
    --id gemma3:27b --intents chat,code --no-key`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" || provider == "" {
				return fmt.Errorf("--id and --provider are required")
			}

			db, err := openDB()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			out := cmd.OutOrStdout()

			// Resolve or create the provider.
			prov, perr := db.Provider(provider)
			if perr != nil {
				if baseURL == "" {
					return fmt.Errorf("provider %q is new; pass --base-url to create it", provider)
				}
				env := keyEnv
				if env == "" && !noKey {
					env = strings.ToUpper(provider) + "_API_KEY"
				}
				prov = database.Provider{Name: provider, BaseURL: baseURL, KeyEnv: env, NeedsKey: !noKey}
				if err := db.UpsertProvider(prov); err != nil {
					return err
				}
				success(out, "added provider %s %s", provider, gray(baseURL))
			} else if baseURL != "" && baseURL != prov.BaseURL {
				// Allow updating an existing provider's endpoint.
				prov.BaseURL = baseURL
				if err := db.UpsertProvider(prov); err != nil {
					return err
				}
				info(out, "updated %s endpoint %s", provider, gray(baseURL))
			}

			m := database.Model{
				Provider:       provider,
				Identifier:     id,
				CostMultiplier: cost,
				IntentTags:     intents,
			}
			if err := db.AddModel(m); err != nil {
				return err
			}

			success(out, "added model %s %s", id,
				gray(fmt.Sprintf("(%s · %s)", provider, firstNonEmpty(intents, "chat"))))
			if !prov.NeedsKey {
				note(out, "local/no-key provider — no API key required")
			} else if needsKeyHint(prov) {
				warn(out, "no key for %s yet — run 'llmroute keys set %s'", provider, provider)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "provider name (existing or new)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "provider chat-completions URL (required for a new provider)")
	cmd.Flags().StringVar(&id, "id", "", "model identifier sent upstream (e.g. gemma3:27b)")
	cmd.Flags().StringVar(&intents, "intents", "chat", "comma-separated intents: chat,code,vision")
	cmd.Flags().StringVar(&keyEnv, "key-env", "", "env var holding the provider key (default <PROVIDER>_API_KEY)")
	cmd.Flags().Float64Var(&cost, "cost", 1.0, "relative cost multiplier (lower is tried first)")
	cmd.Flags().BoolVar(&noKey, "no-key", false, "provider needs no API key (e.g. local Ollama)")
	return cmd
}

func newModelsRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <model-id>",
		Short: "Remove a model from the catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openDB()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := db.RemoveModel(args[0]); err != nil {
				return err
			}
			success(cmd.OutOrStdout(), "removed model %s", args[0])
			return nil
		},
	}
}

func newModelsEnableCmd(enable bool) *cobra.Command {
	verb := "disable"
	if enable {
		verb = "enable"
	}
	return &cobra.Command{
		Use:   verb + " <model-id>",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " a model for routing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openDB()
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := db.SetModelEnabled(args[0], enable); err != nil {
				return err
			}
			success(cmd.OutOrStdout(), "%sd %s", verb, args[0])
			return nil
		},
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// needsKeyHint reports whether a key-requiring provider has neither an env nor
// a stored key yet.
func needsKeyHint(prov database.Provider) bool {
	if !prov.NeedsKey {
		return false
	}
	keys, err := config.LoadKeys()
	if err != nil {
		return true
	}
	return os.Getenv(prov.KeyEnv) == "" && keys.Get(prov.Name) == ""
}
