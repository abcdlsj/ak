package cli

import (
	"os/signal"
	"syscall"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local pool gateway",
		Long: `Run the loopback gateway that provider pools route through.

Every pool command points its engine at this gateway instead of an upstream, so
it must be running for a pool command to work. Normal providers do not use it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if addr == "" {
				addr = cfg.Settings.GatewayAddr
			}
			if addr == "" {
				addr = config.DefaultGatewayAddr
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return gateway.New(cfg).Serve(ctx, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address, host:port (default settings.gateway_addr)")
	return cmd
}
