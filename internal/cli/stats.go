package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/database"
)

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show token usage volume from the local logs",
		Long:  `stats reads usage_logs and prints per-model request counts and token volumes.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := database.DefaultPath()
			if err != nil {
				return err
			}
			db, err := database.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			rows, err := db.Stats()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			header(out, "usage")
			if len(rows) == 0 {
				note(out, "no usage recorded yet")
				return nil
			}

			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, dim("MODEL\tREQUESTS\tPROMPT\tCOMPLETION\tTOTAL"))
			var totReq, totPrompt, totCompletion int64
			for _, r := range rows {
				total := r.PromptTokens + r.CompletionTokens
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n",
					r.Model, r.Requests, r.PromptTokens, r.CompletionTokens, total)
				totReq += r.Requests
				totPrompt += r.PromptTokens
				totCompletion += r.CompletionTokens
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				bold("TOTAL"), bold(fmt.Sprint(totReq)), bold(fmt.Sprint(totPrompt)),
				bold(fmt.Sprint(totCompletion)), bold(fmt.Sprint(totPrompt+totCompletion)))
			return tw.Flush()
		},
	}
}
