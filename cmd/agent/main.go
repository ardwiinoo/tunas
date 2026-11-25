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
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/overlay"
	"github.com/ardwiinoo/tunas/modes"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "open a console window and log to it at debug level")
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	background := flag.Bool("background", false, "started by Windows (autostart): no first-run questions")
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

	if err := run(*cfgPath, *debug, *background); err != nil {
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

// loadingLines reads the waiting texts from modes/mikir.yaml (embedded), with
// %APPDATA%/tunas/modes/*.yaml overriding them without a rebuild.
func loadingLines(log *slog.Logger) overlay.LoadingLines {
	dir := filepath.Join(appDir("APPDATA", os.UserConfigDir), "modes")
	ms, warns, err := modes.LoadWithOverrides(dir)
	for _, w := range warns {
		log.Warn("mode override ignored", "err", w)
	}
	if err != nil {
		log.Warn("modes unavailable, using built-in loading texts", "err", err)
		return overlay.DefaultLoadingLines
	}
	l := ms["mikir"].Lines
	return overlay.LoadingLines{Mikir: l["mikir"], MikirLama: l["mikir_lama"], SangatLama: l["sangat_lama"]}
}

func dataDir() string { return appDir("LOCALAPPDATA", os.UserCacheDir) }

func run(cfgPath string, debug, background bool) error {
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

	log.Info("agent started", "version", version, "server", cfg.ServerURL, "overlay", cfg.Overlay.Enabled, "background", background)
	statePath := filepath.Join(appDir("APPDATA", os.UserConfigDir), "state.json")
	uiState := agent.LoadUIState(statePath)
	client := agent.NewClient(cfg.ServerURL, cfg.DeviceToken)
	a := &agent.Agent{
		Cfg:     cfg,
		Sampler: agent.NewSampler(),
		Buf:     buf,
		API:     client,
		Log:     log,
		Version: version,
	}
	a.SetPaused(uiState.TrackingPaused)

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
	ov.Configure(time.Duration(cfg.Loading.SlowAfterSeconds)*time.Second,
		time.Duration(cfg.Loading.VerySlowAfterSeconds)*time.Second, cfg.Animations, loadingLines(log))
	ov.SetIdleClose(time.Duration(cfg.Bubble.IdleCloseSeconds) * time.Second)
	ov.ConfigureSpeech(time.Duration(cfg.Speech.MsPerChar)*time.Millisecond,
		time.Duration(cfg.Speech.MinSeconds*float64(time.Second)), time.Duration(cfg.Speech.MaxSeconds*float64(time.Second)))
	ov.OnPause = a.SetPaused
	exe, _ := os.Executable()
	ov.SetTray(overlay.TrayOptions{
		Host:  &host{a: a, client: client, statePath: statePath, log: log},
		State: uiState, Registry: agent.UserRunKey{}, ExePath: exe,
		ConfigDir:      appDir("APPDATA", os.UserConfigDir),
		OfferAutostart: !background && !uiState.AutostartOffered,
	})
	a.OnWorklogQueue = ov.SetWorklogQueue
	a.OnWorklog = func(e agent.WorklogEvent) {
		if agent.ShowWorklogBubble(e, a.IdleFor(), a.CurrentForm().Form, cfg.WorklogBubbleInFocus) {
			ov.WorklogEvent(e)
		} else {
			log.Info("worklog bubble skipped (idle, tidur, fokus or stale); Telegram has it", "prompt", e.PromptID)
		}
	}
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

// host is what the tray and the worklog bubble need from the agent.
type host struct {
	a         *agent.Agent
	client    *agent.Client
	statePath string
	log       *slog.Logger
}

func (h *host) WriteWorklog(ctx context.Context, text string, promptID int64) (agent.WriteResult, error) {
	return h.a.WriteWorklog(ctx, text, promptID)
}

func (h *host) AnswerPrompt(ctx context.Context, id int64, action string) error {
	return h.client.AnswerPrompt(ctx, id, action)
}

func (h *host) MuteWorklog(ctx context.Context) error { return h.client.MuteWorklog(ctx) }

func (h *host) Status() string { return h.a.StatusLine(time.Now()) }

func (h *host) SaveState(st agent.UIState) {
	if err := agent.SaveUIState(h.statePath, st); err != nil {
		h.log.Warn("state.json not saved", "err", err)
	}
}
