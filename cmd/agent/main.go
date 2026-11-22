// Command tunas-agent runs on the owner's Windows laptop: it tracks the
// foreground app, syncs activity to the server and (later) shows the overlay.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "log to console at debug level")
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	flag.Parse()

	if err := run(*cfgPath, *debug); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func appDir(env string, fallback func() (string, error)) string {
	dir := os.Getenv(env)
	if dir == "" {
		if d, err := fallback(); err == nil {
			dir = d
		}
	}
	return filepath.Join(dir, "tunas")
}

func defaultConfigPath() string {
	return filepath.Join(appDir("APPDATA", os.UserConfigDir), "config.yaml")
}

func dataDir() string { return appDir("LOCALAPPDATA", os.UserCacheDir) }

func run(cfgPath string, debug bool) error {
	var out io.Writer = os.Stderr
	level := slog.LevelDebug
	if !debug {
		level = slog.LevelInfo
		lf, err := agent.OpenRotatingFile(filepath.Join(dataDir(), "agent.log"), 5<<20)
		if err != nil {
			return err
		}
		defer lf.Close()
		out = lf
	}
	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))

	cfg, err := config.LoadAgentFile(cfgPath)
	if err != nil {
		log.Error("config error", "path", cfgPath, "err", err)
		return fmt.Errorf("load %s: %w", cfgPath, err)
	}
	if cfg.DeviceToken == "" {
		err := errors.New("device_token is empty in config.yaml (create one with: tunas-server device create --name laptop)")
		log.Error("config error", "err", err)
		return err
	}

	buf, err := agent.OpenBuffer(filepath.Join(dataDir(), "buffer.db"))
	if err != nil {
		log.Error("buffer error", "err", err)
		return err
	}
	defer buf.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("agent started", "version", version, "server", cfg.ServerURL)
	a := &agent.Agent{
		Cfg:     cfg,
		Sampler: agent.NewSampler(),
		Buf:     buf,
		API:     agent.NewClient(cfg.ServerURL, cfg.DeviceToken),
		Log:     log,
	}
	err = a.Run(ctx)
	log.Info("agent stopped")
	return err
}
