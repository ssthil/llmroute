package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ssthil/llmroute/internal/config"
	"github.com/ssthil/llmroute/internal/database"
	"github.com/ssthil/llmroute/internal/network"
	"github.com/ssthil/llmroute/internal/router"
)

func newProxyCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Start the local routing proxy gateway",
		Long: `proxy boots the loopback HTTP gateway on 127.0.0.1. If the requested
port is busy it scans upward until it finds a free one. Inbound requests to
/v1/chat/completions are screened for leaked credentials, classified by intent,
and routed to the cheapest capable upstream provider with automatic failover.

Provider keys are read from the environment:
  OPENAI_API_KEY, GEMINI_API_KEY, ANTHROPIC_API_KEY, DEEPSEEK_API_KEY`,
		Args: cobra.NoArgs,
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

			keys, err := config.LoadKeys()
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 5 * time.Minute}
			rtr := router.New(db, client)
			rtr.SetFileKeys(keys.Providers)

			printProxyBanner(cmd.OutOrStdout(), db, keys)

			logger := log.New(cmd.OutOrStdout(), "", log.LstdFlags)
			srv := network.NewServer(db, rtr, logger)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return srv.ListenAndServe(ctx, port)
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", network.DefaultPort, "preferred loopback port (scans upward if busy)")
	return cmd
}

// printProxyBanner shows a concise readiness summary: enabled models and which
// providers actually have a usable key.
func printProxyBanner(out io.Writer, db *database.DB, keys *config.Keys) {
	header(out, "proxy")

	models, err := db.EnabledModels()
	if err != nil {
		return
	}
	providers, _ := db.ProvidersMap()

	ready := 0
	for _, m := range models {
		p := providers[m.Provider]
		if !p.NeedsKey || os.Getenv(p.KeyEnv) != "" || keys.Get(p.Name) != "" {
			ready++
		}
	}
	info(out, "%d models enabled, %s ready to route", len(models),
		fmt.Sprintf("%d", ready))
	if ready == 0 {
		warn(out, "no model has a usable key — run 'llmroute init' or 'llmroute keys set <provider>'")
	}
}
