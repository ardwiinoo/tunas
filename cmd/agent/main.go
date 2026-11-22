// Command tunas-agent runs on the owner's Windows laptop: it tracks the
// foreground app, syncs activity to the server and (later) shows the overlay.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

func defaultConfigPath() string {
	dir := os.Getenv("APPDATA")
	if dir == "" {
		if d, err := os.UserConfigDir(); err == nil {
			dir = d
		}
	}
	return filepath.Join(dir, "tunas", "config.yaml")
}

func run(cfgPath string, debug bool) error {
	cfg, err := config.LoadAgentFile(cfgPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", cfgPath, err)
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	log.Info("agent started", "version", version, "server", cfg.ServerURL, "token_set", cfg.DeviceToken != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("agent stopped")
	return nil
}
