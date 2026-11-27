// Command tunas-agent runs on the owner's Windows laptop: it tracks the
// foreground app, syncs activity to the server and (later) shows the overlay.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
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
	"github.com/ardwiinoo/tunas/internal/projects"
	"github.com/ardwiinoo/tunas/modes"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "open a console window and log to it at debug level")
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.yaml")
	background := flag.Bool("background", false, "started by Windows (autostart): no first-run questions")
	probeTitle := flag.Bool("probe-title", false, "print the active window's process and title to the console for 10 seconds (local only, nothing is stored) to fit an editor pattern in config.yaml")
	showStatus := flag.Bool("status", false, "print the running agent's own statistics as JSON (no token, no network) and exit")
	setTok := flag.Bool("set-token", false, "store the device token encrypted (Windows DPAPI): it is read from stdin, never from an argument")
	setKey := flag.String("set-client-key", "", "store the mTLS client key (PEM file) encrypted (Windows DPAPI)")
	flag.Parse()
	if *probeTitle {
		if _, err := agent.OpenConsole("Tunas agent: probe-title"); err != nil {
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		projects.Probe(ctx, os.Stdout, projects.NewReader(), 10*time.Second)
		stop()
		fmt.Println("Tekan Enter untuk menutup...")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	if *setTok || *setKey != "" {
		own, err := agent.OpenConsole("Tunas agent: simpan rahasia")
		if err != nil {
			os.Exit(1)
		}
		if *setTok {
			err = setToken(*cfgPath)
		} else {
			err = setClientKey(*setKey)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		if own {
			pressEnter()
		}
		if err != nil {
			os.Exit(1)
		}
		return
	}
	if *showStatus {
		out, err := agent.ReadStatsFile(statsPath(), time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		return
	}

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

// statsPath is where the running agent leaves its latest statistics.
func statsPath() string { return filepath.Join(dataDir(), "stats.json") }

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
	// The device token lives encrypted (DPAPI); a plain one still in
	// config.yaml is moved there. It is kept in this local only.
	token, err := agent.LoadDeviceToken(cfgPath, cfg.DeviceToken, tokenStore(), log)
	if err != nil {
		log.Error("device token", "err", err)
		return err
	}
	cfg.DeviceToken = ""
	var certs []tls.Certificate
	var certWarn string
	if cfg.TLS.ClientCert != "" {
		cc, err := agent.LoadClientCert(cfg.TLS.ClientCert, cfg.TLS.ClientKey, clientKeyStore(), time.Now())
		if err != nil {
			log.Error("client certificate", "err", err)
			return err
		}
		certs = append(certs, cc.Cert)
		if certWarn = cc.Warn(time.Now()); certWarn != "" {
			log.Warn(certWarn)
		}
		log.Info("mTLS client certificate loaded", "expires", cc.NotAfter.Format("2006-01-02"))
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
	if uiState.ExtensionToken == "" {
		uiState.ExtensionToken = agent.NewExtensionToken()
		if err := agent.SaveUIState(statePath, uiState); err != nil {
			log.Warn("state.json not saved", "err", err)
		}
	}
	client := agent.NewClient(cfg.ServerURL, token, certs...)
	a := &agent.Agent{
		Cfg:            cfg,
		Sampler:        agent.NewSampler(),
		Buf:            buf,
		API:            client,
		Log:            log,
		Version:        version,
		Proc:           agent.NewProcReader(),
		StatsFile:      statsPath(),
		CategoriesFile: filepath.Join(dataDir(), "categories.json"),
	}
	a.SetCertWarning(certWarn)
	a.SetPaused(uiState.TrackingPaused)
	if ext, err := projects.NewExtractor(cfg.Projects.Editors); err == nil { // validated with the config
		a.Extractor, a.Titles = ext, projects.NewReader()
	}
	a.SetProjects(uiState.ProjectsEnabled)
	a.SetBrowsers(cfg.Extension.Browsers)
	a.SetDomains(uiState.DomainsEnabled)
	br := &bridgeCtl{parent: ctx, cfg: cfg.Extension, token: uiState.ExtensionToken, a: a, log: log}
	defer br.Set(false)
	br.Set(uiState.DomainsEnabled)

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
	a.OverlayState = ov.State
	ov.Configure(time.Duration(cfg.Loading.SlowAfterSeconds)*time.Second,
		time.Duration(cfg.Loading.VerySlowAfterSeconds)*time.Second, cfg.Animations, loadingLines(log))
	ov.SetIdleClose(time.Duration(cfg.Bubble.IdleCloseSeconds) * time.Second)
	ov.ConfigureSpeech(time.Duration(cfg.Speech.MsPerChar)*time.Millisecond,
		time.Duration(cfg.Speech.MinSeconds*float64(time.Second)), time.Duration(cfg.Speech.MaxSeconds*float64(time.Second)))
	ov.OnPause = a.SetPaused
	ov.OnProjects = a.SetProjects
	ov.OnDomains = func(on bool) {
		a.SetDomains(on)
		br.Set(on)
	}
	a.OnNudge = ov.Nudge
	ov.OnTegur = a.TegurAnswer
	exe, _ := os.Executable()
	ov.SetTray(overlay.TrayOptions{
		Host: &host{token: uiState.ExtensionToken, a: a, client: client, statePath: statePath, log: log,
			cal: &agent.Calendar{API: client, Cache: &agent.CalCache{Path: filepath.Join(dataDir(), "calendar.json"), Now: time.Now}}},
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
	token     string
	a         *agent.Agent
	client    *agent.Client
	statePath string
	log       *slog.Logger
	cal       *agent.Calendar
}

// calSource is the calendar card's view of the agent: the server first, the
// cache when offline, and entries written through the offline queue.
type calSource struct {
	*agent.Calendar
	a *agent.Agent
}

func (c calSource) AddOn(ctx context.Context, date, text string) (agent.WriteResult, error) {
	res, err := c.a.WriteWorklogOn(ctx, date, text, 0)
	c.ForgetCached()
	return res, err
}

func (h *host) ExtensionToken() string { return h.token }

func (h *host) Calendar() overlay.CalendarSource { return calSource{h.cal, h.a} }

func (h *host) Command(ctx context.Context, cmd, args string) (agent.CommandResult, error) {
	return h.client.RunCommand(ctx, cmd, args)
}

func (h *host) WriteWorklog(ctx context.Context, text string, promptID int64) (agent.WriteResult, error) {
	return h.a.WriteWorklog(ctx, text, promptID)
}

func (h *host) AnswerPrompt(ctx context.Context, id int64, action string) error {
	return h.client.AnswerPrompt(ctx, id, action)
}

func (h *host) MuteWorklog(ctx context.Context) error { return h.client.MuteWorklog(ctx) }

func (h *host) StatusReport(ctx context.Context) string { return h.a.StatusReport(ctx, h.client) }

func (h *host) Status() string { return h.a.StatusLine(time.Now()) }

func (h *host) SaveState(st agent.UIState) {
	if err := agent.SaveUIState(h.statePath, st); err != nil {
		h.log.Warn("state.json not saved", "err", err)
	}
}
