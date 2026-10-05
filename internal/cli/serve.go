package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

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
it must be running for a pool command to work. Normal providers do not use it.

The gateway reloads providers.toml when it changes (or on SIGHUP), so an edited
or renamed pool takes effect without a restart. Changing settings.gateway_addr
still needs a restart, since that is where it listens.`,
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

			srv := gateway.New(cfg)
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			defer signal.Stop(hup)
			go watchConfig(ctx, srv, hup)
			return srv.Serve(ctx, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address, host:port (default settings.gateway_addr)")
	return cmd
}

// watchConfig reloads the gateway when providers.toml changes, so an edited
// pool is picked up without a restart. A bad edit is ignored, leaving the
// running config in place.
func watchConfig(ctx context.Context, srv *gateway.Server, hup <-chan os.Signal) {
	path, err := config.Path()
	if err != nil {
		return
	}
	last := modTime(path)
	reload := func() {
		m := modTime(path)
		if m.IsZero() {
			return
		}
		last = m
		cfg, err := config.Load()
		if err != nil {
			srv.Log("gateway: reload skipped: %v", err)
			return
		}
		if err := config.Validate(cfg); err != nil {
			srv.Log("gateway: reload skipped: %v", err)
			return
		}
		srv.Reload(cfg)
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			reload()
		case <-t.C:
			if m := modTime(path); !m.IsZero() && !m.Equal(last) {
				reload()
			}
		}
	}
}

func modTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
