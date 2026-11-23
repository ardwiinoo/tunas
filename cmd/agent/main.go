// Command tunas-agent runs on the owner's Windows laptop: it tracks the
// foreground app, syncs activity to the server and (later) shows the overlay.
package main

import (
	"bufio"
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
	"github.com/ardwiinoo/tunas/internal/overlay"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "open a console window and log to it at debug level")
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	flag.Parse()

	// The release build is a GUI-subsystem exe (no console), so --debug has
	// to create one or its output would go nowhere.
	ownConsole := false
	if *debug {
		var err error
		if ownConsole, err = agent.OpenConsole("Tunas agent (debug)"); err != nil {
			os.Exit(1) // nowhere to report it
		}
	}

	if err := run(*cfgPath, *debug); err != nil {
		if errors.Is(err, agent.ErrAlreadyRunning) {
			fmt.Fprintln(os.Stderr, "Tunas agent sudah berjalan. Hentikan yang lama dulu (Task Manager → tunas-agent.exe).")
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		if ownConsole {
			// the console closes with the process; keep the error readable
			fmt.Fprintln(os.Stderr, "Tekan Enter untuk menutup...")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
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
	// One agent per user session: two would double-count activity and draw
	// two overlays (two bubbles on screen at once).
	release, err := agent.SingleInstance("TunasAgent")
	if err != nil {
		return err
	}
	defer release()

	var out io.Writer = os.Stderr
	level := slog.LevelDebug
	logPath := "" // --debug logs to the console
	if !debug {
		level = slog.LevelInfo
		logPath = filepath.Join(dataDir(), "agent.log")
		lf, err := agent.OpenRotatingFile(logPath, 5<<20)
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

	log.Info("agent started", "version", version, "server", cfg.ServerURL, "overlay", cfg.Overlay.Enabled)
	client := agent.NewClient(cfg.ServerURL, cfg.DeviceToken)
	a := &agent.Agent{
		Cfg:     cfg,
		Sampler: agent.NewSampler(),
		Buf:     buf,
		API:     client,
		Log:     log,
		Version: version,
	}

	// The window also carries the tray icon, so it runs even when the
	// character is disabled in config.yaml (tray only).
	ov, err := overlay.New(cfg.Overlay, client, log)
	if err != nil {
		log.Warn("overlay and tray unavailable", "err", err)
		ov = nil
	}
	if ov == nil {
		err = a.Run(ctx)
		log.Info("agent stopped")
		return err
	}

	ov.CharacterOff = !cfg.Overlay.Enabled
	ov.OnPause = a.SetPaused
	ov.OnQuit = stop
	ov.LogPath = logPath
	a.OnForm = ov.Notify
	a.OnConn = ov.SetConn
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	if err := ov.Run(ctx); err != nil { // blocks on the main goroutine
		log.Error("overlay stopped", "err", err)
		stop()
	}
	err = <-done
	log.Info("agent stopped")
	return err
}
