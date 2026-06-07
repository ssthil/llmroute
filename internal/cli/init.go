package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/database"
	"github.com/ssthil/llmroute/internal/security"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize the config directory and seed the local database",
		Long: `init creates ~/.config/llmroute with 0700 permissions, provisions the
records.db SQLite file with 0600 permissions, runs migrations, and seeds the
baseline routing model catalog.`,
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

			models, err := db.AllModels()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "config dir : %s (0700)\n", dir)
			fmt.Fprintf(out, "database   : %s (0600)\n", path)
			fmt.Fprintf(out, "seeded %d models:\n", len(models))
			for _, m := range models {
				fmt.Fprintf(out, "  - %-22s provider=%-10s cost=%.2f intents=[%s]\n",
					m.Identifier, m.Provider, m.CostMultiplier, m.IntentTags)
			}
			return nil
		},
	}
}
