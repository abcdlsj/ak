package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
it must be running for a pool command to work; a pool command starts it in the
background when it is not, logging to ~/.local/share/ak/gateway.log. Normal
providers do not use it.

The gateway reloads providers.toml when it changes (or on SIGHUP), so an edited
or renamed pool takes effect without a restart. Changing settings.gateway_addr
still needs a restart, since that is where it listens.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if addr == "" {
				addr = gatewayAddr(cfg)
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

func gatewayAddr(cfg *config.Config) string {
	if cfg.Settings.GatewayAddr != "" {
		return cfg.Settings.GatewayAddr
	}
	return config.DefaultGatewayAddr
}

// ensureGateway starts a detached `ak serve` unless addr already answers, and
// waits for it to listen. Its output goes to gateway.log in ak's data dir.
func ensureGateway(addr string) error {
	if gatewayUp(addr) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := config.DataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(dir, "gateway.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	c := exec.Command(exe, "serve")
	c.Stdout, c.Stderr = logf, logf
	// Its own session, so closing the terminal or the agent does not stop it.
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return fmt.Errorf("start ak serve: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()
	deadline := time.After(5 * time.Second)
	for {
		if gatewayUp(addr) {
			fmt.Fprintf(os.Stderr, "ak: started the pool gateway on %s (log: %s)\n", addr, logPath)
			return nil
		}
		select {
		case <-exited:
			// Another command may have started it first.
			if gatewayUp(addr) {
				return nil
			}
			return fmt.Errorf("ak serve exited; see %s", logPath)
		case <-deadline:
			return fmt.Errorf("ak serve did not listen on %s within 5s; see %s", addr, logPath)
		case <-time.After(100 * time.Millisecond):
		}
	}
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
