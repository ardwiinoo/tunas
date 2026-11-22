// Command tunas-server runs the Tunas server (Telegram bot, scheduler, agent API).
//
// Subcommands:
//
//	serve (default)                run the server
//	healthcheck                    exit 0 if the local /healthz responds ok
//	device create|list|revoke      manage agent device tokens
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ardwiinoo/tunas/internal/api"
	"github.com/ardwiinoo/tunas/internal/assist"
	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/forms"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/reminders"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/modes"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "device":
		err = deviceCmd(args, os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q (use serve, healthcheck, device)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func serve() error {
	cfg, err := config.LoadServer(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := openDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "new", applied)

	ms, err := modes.Load()
	if err != nil {
		return err
	}
	var completer llm.Completer = llm.Disabled{}
	if cfg.LLMEnabled() {
		completer = &llm.Capped{
			Inner: llm.New(cfg.BifrostBaseURL, cfg.BifrostAPIKey, cfg.LLMPrimaryModel, cfg.LLMFallbackModels, log),
			Usage: db, Cap: cfg.LLMDailyRequestCap, Now: time.Now, Loc: cfg.Location,
		}
	}
	log.Info("llm", "enabled", cfg.LLMEnabled(), "primary", cfg.LLMPrimaryModel, "fallbacks", len(cfg.LLMFallbackModels))
	asst := assist.New(db, completer, ms, cfg.Location, time.Now, log)

	cmds := &bot.Commands{DB: db, Loc: cfg.Location, Now: time.Now, Log: log}
	cmds.Extra = func(ctx context.Context, cmd, args string) (string, bool) {
		switch cmd {
		case "tanya":
			return asst.Ask(ctx, args), true
		case "form":
			reply, err := asst.SetForm(ctx, args)
			if err != nil {
				log.Error("set form failed", "err", err)
				return "Maaf, gagal mengatur form.", true
			}
			return reply, true
		}
		return "", false
	}
	tgBot, err := bot.NewTelegram(cfg.TelegramBotToken, cfg.TelegramOwnerID, cmds, log)
	if err != nil {
		return err
	}
	tgBot.OnText = asst.HandleText
	tgBot.OnCallback = asst.HandleCallback
	sched := reminders.NewScheduler(db, tgBot, time.Now, cfg.Location, log)

	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.New(log, api.Options{DB: db, Chat: func(ctx context.Context, form, text string) string {
			f := forms.Form(form)
			if !forms.Valid(f) {
				f = asst.ActiveForm(ctx)
			}
			return asst.ChatAs(ctx, f, text)
		}}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	wg.Go(func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	})
	wg.Go(func() { sched.Run(ctx) })
	wg.Go(func() { _ = tgBot.Run(ctx) })
	wg.Go(func() { asst.RunDaily(ctx, tgBot) })

	select {
	case <-ctx.Done():
	case err = <-errCh:
		stop()
	}
	log.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shCtx)
	wg.Wait()
	return err
}

func openDB(ctx context.Context, url string) (*store.DB, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return store.Open(ctx, url, 4)
}

// healthcheck hits the local /healthz. Used by the Docker healthcheck (image has no curl).
func healthcheck() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad HTTP_ADDR: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != "ok" {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

const deviceUsage = "usage: device create --name NAME | device list | device revoke --name NAME"

func deviceCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(deviceUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("device "+sub, flag.ContinueOnError)
	name := fs.String("name", "", "device name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := openDB(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		return err
	}

	switch sub {
	case "create":
		if *name == "" || len(*name) > 64 {
			return errors.New("--name is required (max 64 chars)")
		}
		d, token, err := db.CreateDevice(ctx, *name)
		if errors.Is(err, store.ErrExists) {
			return fmt.Errorf("device %q already exists", *name)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "device %q created (id %d)\n", d.Name, d.ID)
		fmt.Fprintf(out, "token (shown once, store it in config.yaml as device_token):\n%s\n", token)
	case "list":
		ds, err := db.ListDevices(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%-4s %-20s %-8s %-25s %s\n", "ID", "NAME", "STATUS", "LAST SEEN", "CREATED")
		for _, d := range ds {
			status, seen := "active", "-"
			if d.RevokedAt != nil {
				status = "revoked"
			}
			if d.LastSeenAt != nil {
				seen = d.LastSeenAt.Format(time.RFC3339)
			}
			fmt.Fprintf(out, "%-4d %-20s %-8s %-25s %s\n", d.ID, d.Name, status, seen, d.CreatedAt.Format(time.RFC3339))
		}
	case "revoke":
		if *name == "" {
			return errors.New("--name is required")
		}
		err := db.RevokeDevice(ctx, *name, time.Now())
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no active device named %q", *name)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "device %q revoked\n", *name)
	default:
		return errors.New(deviceUsage)
	}
	return nil
}
