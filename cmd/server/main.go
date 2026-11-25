// Command tunas-server runs the Tunas server (Telegram bot, scheduler, agent API).
//
// Subcommands:
//
//	serve (default)                run the server
//	healthcheck                    exit 0 if the local /healthz responds ok
//	device create|list|revoke      manage agent device tokens
package main

import (
	"bytes"
	"compress/gzip"
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
	"github.com/ardwiinoo/tunas/internal/apps"
	"github.com/ardwiinoo/tunas/internal/assist"
	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/eod"
	"github.com/ardwiinoo/tunas/internal/forms"
	"github.com/ardwiinoo/tunas/internal/ghbackup"
	"github.com/ardwiinoo/tunas/internal/holidays"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/llmstats"
	"github.com/ardwiinoo/tunas/internal/ops"
	"github.com/ardwiinoo/tunas/internal/projreport"
	"github.com/ardwiinoo/tunas/internal/reminders"
	"github.com/ardwiinoo/tunas/internal/search"
	"github.com/ardwiinoo/tunas/internal/status"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/sysstats"
	"github.com/ardwiinoo/tunas/internal/worklog"
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
	case "worklog":
		err = worklogCmd(args, os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q (use serve, healthcheck, device, worklog)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

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
	var tgBot *bot.Telegram // assigned below; used by monitor/export closures
	monitor := ops.NewMonitor(db, notifierFunc(func(ctx context.Context, text string) error {
		return tgBot.NotifyOwner(ctx, text)
	}), time.Now, cfg.Location, log)
	llmTrack := &llmstats.Tracker{Store: db, Now: time.Now, Loc: cfg.Location}
	var completer llm.Completer = llm.Disabled{}
	if cfg.LLMEnabled() {
		llmClient := llm.New(cfg.BifrostBaseURL, cfg.BifrostAPIKey, cfg.LLMPrimaryModel, cfg.LLMFallbackModels, log)
		llmClient.OnBlocked = monitor.ModelBlocked
		llmClient.OnAttempt = llmTrack.Record
		completer = &llm.Capped{
			Inner: monitor.Watch(llmClient),
			Usage: db, Cap: cfg.LLMDailyRequestCap, Now: time.Now, Loc: cfg.Location,
		}
	}
	log.Info("llm", "enabled", cfg.LLMEnabled(), "primary", cfg.LLMPrimaryModel, "fallbacks", len(cfg.LLMFallbackModels))
	asst := assist.New(db, completer, ms, cfg.Location, time.Now, log)
	if cfg.TavilyAPIKey != "" {
		asst.Searcher = search.NewTavily(cfg.TavilyAPIKey)
		asst.Credits = &search.Credits{DB: db, Now: time.Now, Loc: cfg.Location}
	} else {
		log.Info("web search off (TAVILY_API_KEY empty); /cari says so")
	}

	statusSvc := &status.Service{
		DB: db, Server: &sysstats.Collector{DB: db, Version: version, Start: time.Now(), Now: time.Now},
		HostFile: cfg.HostStatsFile, LLM: llmTrack, LLMEnabled: cfg.LLMEnabled(), Primary: cfg.LLMPrimaryModel,
		LLMCap: cfg.LLMDailyRequestCap, Version: version, Now: time.Now, Loc: cfg.Location,
	}
	if asst.Credits != nil {
		statusSvc.Credits = func(ctx context.Context) string {
			line, _ := asst.Credits.Line(ctx)
			return line
		}
	}
	monitor.Res = statusSvc
	projSvc := &projreport.Service{DB: db, Now: time.Now, Loc: cfg.Location}
	appsSvc := &apps.Service{DB: db, Now: time.Now, Loc: cfg.Location, Log: log}
	if n, err := appsSvc.Seed(ctx, config.DefaultAppSeed()); err != nil {
		return err
	} else if n > 0 {
		log.Info("app categories seeded", "new", n)
	}
	log.Info("status", "host_stats", cfg.HostStatsFile != "", "version", version)

	cmds := &bot.Commands{DB: db, Loc: cfg.Location, Now: time.Now, Log: log}
	if asst.Credits != nil {
		cmds.StatusExtra = func(ctx context.Context) string {
			line, err := asst.Credits.Line(ctx)
			if err != nil {
				return ""
			}
			return "🔎 " + line
		}
	}
	cmds.Extra = func(ctx context.Context, cmd, args string) (string, bool) {
		switch cmd {
		case "tanya":
			return asst.Ask(ctx, args), true
		case "cari":
			return asst.SearchText(ctx, args), true
		case "export":
			return exportData(ctx, db, tgBot, log), true
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
	tgBot, err = bot.NewTelegram(cfg.TelegramBotToken, cfg.TelegramOwnerID, cmds, log)
	if err != nil {
		return err
	}
	wl := &worklog.Service{DB: db, Loc: cfg.Location, Now: time.Now, Log: log, ChatID: cfg.TelegramOwnerID}
	eodSvc := &eod.Service{DB: db, Now: time.Now, Loc: cfg.Location, Log: log, Worklog: eodWorklog{wl}}
	wl.Send = func(ctx context.Context, r bot.Reply) error { return tgBot.SendReply(ctx, cfg.TelegramOwnerID, r) }
	wl.SendDoc = func(ctx context.Context, name string, data []byte, caption string) error {
		return tgBot.SendDocument(ctx, name, bytes.NewReader(data), caption)
	}
	if cfg.LLMEnabled() {
		wl.LLM = completer // /rekap ai only, counted in llm_usage
	}
	rem := &worklog.Reminder{Svc: wl, TG: tgBot}
	var backup *ghbackup.Worker
	if cfg.GitHubBackupToken != "" && cfg.GitHubBackupRepo != "" {
		gh, err := ghbackup.NewClient(cfg.GitHubBackupToken, cfg.GitHubBackupRepo, cfg.GitHubBackupBranch, cfg.GitHubBackupDir)
		if err != nil {
			return err
		}
		backup = &ghbackup.Worker{GH: gh, DB: db, Now: time.Now, Log: log,
			Notify: func(ctx context.Context, text string) error { return tgBot.NotifyOwner(ctx, text) }}
		rem.Sync = backup.SyncNow
	} else {
		log.Info("worklog GitHub backup off (GITHUB_BACKUP_TOKEN/GITHUB_BACKUP_REPO empty)")
	}
	wl.OnSaved = rem.OnSaved
	wl.ExtraCommand = func(ctx context.Context, cmd, args string) (bot.Reply, bool) {
		if cmd == "worklog" {
			return rem.Command(ctx, args), true
		}
		return bot.Reply{}, false
	}
	wl.ExtraCallback = rem.Callback
	tgBot.OnCommand = func(ctx context.Context, cmd, args string) (bot.Reply, bool) {
		if cmd == "status" {
			return statusReply(ctx, statusSvc, args, log), true
		}
		switch cmd {
		case "proyek":
			text, err := projSvc.Command(ctx, args)
			if err != nil {
				log.Error("proyek command failed", "err", err)
				return bot.Reply{Text: "Maaf, ada gangguan di server. Coba lagi sebentar."}, true
			}
			return bot.Reply{Text: text}, true
		case "laporan":
			card, err := eodSvc.Command(ctx, args)
			if err != nil {
				log.Error("laporan command failed", "err", err)
				return bot.Reply{Text: "Maaf, ada gangguan di server. Coba lagi sebentar."}, true
			}
			return eodReply(card), true
		}
		if card, ok, err := appsSvc.Command(ctx, cmd, args); ok {
			if err != nil {
				log.Error("apps command failed", "cmd", cmd, "err", err)
				return bot.Reply{Text: "Maaf, ada gangguan di server. Coba lagi sebentar."}, true
			}
			return appsReply(card), true
		}
		return wl.Command(ctx, cmd, args)
	}
	tgBot.OnText = func(ctx context.Context, text string) bot.Reply {
		if r, ok := wl.PendingText(ctx, text); ok { // an answer to a worklog button
			return r
		}
		return asst.HandleText(ctx, text)
	}
	tgBot.OnCallback = func(ctx context.Context, data string) bot.Reply {
		if card, ok, err := eodSvc.Callback(ctx, data); ok {
			if err != nil {
				log.Error("laporan callback failed", "err", err)
				return bot.Reply{Toast: "Gagal menambahkan.", Keep: true}
			}
			r := eodReply(card)
			r.Keep = r.Text == ""
			return r
		}
		if card, ok, err := appsSvc.Callback(ctx, data); ok {
			if err != nil {
				log.Error("apps callback failed", "err", err)
				return bot.Reply{Toast: "Gagal menyimpan kategori.", Keep: true}
			}
			r := appsReply(card)
			r.Keep = r.Text == ""
			return r
		}
		if c, ok, err := statusSvc.Callback(ctx, data); ok {
			if err != nil {
				log.Error("status callback failed", "err", err)
				return bot.Reply{Toast: "Gagal mengambil status.", Keep: true}
			}
			return cardReply(c)
		}
		if r, ok := wl.Callback(ctx, data); ok {
			return r
		}
		return bot.Reply{Text: asst.HandleCallback(ctx, data)}
	}
	sched := reminders.NewScheduler(db, tgBot, time.Now, cfg.Location, log)

	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.New(log, api.Options{DB: db, StatusSummary: statusSvc.Summary,
			Calendar: calendarAPI{wl},
			Command: func(ctx context.Context, cmd, args string) (string, bool) {
				return laptopCommand(ctx, cmd, args, cmds, statusSvc, appsSvc, wl, cfg.Location)
			},
			// the bubble uses the same pipeline as Telegram (intent, fixed
			// queries, confirmation), plus "cari ..." for web search
			Chat: func(ctx context.Context, form, text string, history []api.ChatTurn) (api.ChatResult, error) {
				// /cari from the bubble (the allowed command; "cari ..." works too)
				if q, ok := searchQuery(text); ok {
					r := asst.Search(ctx, q)
					res := api.ChatResult{Reply: r.Text, Search: r.Searched}
					for _, s := range r.Sources {
						res.Sources = append(res.Sources, api.ChatSource{Title: s.Title, URL: s.URL})
					}
					return res, nil
				}
				f := forms.Form(form)
				if !forms.Valid(f) {
					f = asst.ActiveForm(ctx)
				}
				hist := make([]llm.Message, len(history))
				for i, t := range history {
					hist[i] = llm.Message{Role: t.Role, Content: t.Text}
				}
				r, err := asst.Handle(ctx, f, text, hist)
				return api.ChatResult{Reply: r.Text, ConfirmID: assist.ConfirmID(r)}, err
			},
			Confirm: asst.Confirm,
			Worklog: worklogAPI{svc: wl, rem: rem},
		}),
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
	eodSvc.Send = func(ctx context.Context, c eod.Card) error {
		return tgBot.SendReply(ctx, cfg.TelegramOwnerID, eodReply(c))
	}
	wg.Go(func() { eodSvc.Run(ctx) })
	appsSvc.Send = func(ctx context.Context, c apps.Card) error {
		return tgBot.SendReply(ctx, cfg.TelegramOwnerID, appsReply(c))
	}
	wg.Go(func() { appsSvc.Run(ctx) })
	wg.Go(func() { _ = tgBot.Run(ctx) })
	wg.Go(func() { asst.RunDaily(ctx, tgBot) })
	wg.Go(func() { monitor.Run(ctx, time.Minute) }) // offline notice ~15 min after the last contact
	hol := &holidays.Refresher{API: holidays.NewClient(cfg.HolidayAPIBase), DB: db, Now: time.Now, Loc: cfg.Location, Log: log,
		Notify: func(ctx context.Context, text string) error { return tgBot.NotifyOwner(ctx, text) }}
	wg.Go(func() { hol.Run(ctx) })
	wg.Go(func() { rem.Run(ctx) })
	if backup != nil {
		wg.Go(func() { backup.Run(ctx) })
	}

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

const worklogUsage = `usage: tunas-server worklog import [--dry-run] [--repo owner/repo] [--dir sub] [--branch main]
  Reads every YYYYMM_daily.txt from the backup repo into the database (idempotent).
  The token comes only from GITHUB_BACKUP_TOKEN; repo/dir/branch default to GITHUB_BACKUP_*.`

// worklogCmd is "worklog import": the one-time import of the old app's files.
func worklogCmd(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "import" {
		return errors.New(worklogUsage)
	}
	fs := flag.NewFlagSet("worklog import", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "read and count, write nothing")
	repo := fs.String("repo", os.Getenv("GITHUB_BACKUP_REPO"), "owner/repo")
	dir := fs.String("dir", os.Getenv("GITHUB_BACKUP_DIR"), "folder in the repo")
	branch := fs.String("branch", os.Getenv("GITHUB_BACKUP_BRANCH"), "branch (default main)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_BACKUP_TOKEN"))
	if token == "" {
		return errors.New("GITHUB_BACKUP_TOKEN is required")
	}
	gh, err := ghbackup.NewClient(token, *repo, *branch, *dir)
	if err != nil {
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
	_, err = ghbackup.Import(ctx, gh, db, *dry, time.Now(), out)
	return err
}

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

type notifierFunc func(ctx context.Context, text string) error

func (f notifierFunc) NotifyOwner(ctx context.Context, text string) error { return f(ctx, text) }

// exportData sends all owner data as a gzipped JSON document.
func exportData(ctx context.Context, db *store.DB, tg *bot.Telegram, log *slog.Logger) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	now := time.Now()
	if err := db.Export(ctx, zw, now); err != nil {
		log.Error("export failed", "err", err)
		return "Maaf, ekspor gagal."
	}
	if err := zw.Close(); err != nil {
		return "Maaf, ekspor gagal."
	}
	if buf.Len() > 45<<20 {
		return "Data terlalu besar untuk Telegram (> 45 MB). Pakai backup pg_dump di VPS."
	}
	name := "tunas-export-" + now.Format("20060102-1504") + ".json.gz"
	if err := tg.SendDocument(ctx, name, &buf, "📦 Ekspor data Tunas"); err != nil {
		log.Error("export upload failed", "err", err)
		return "Maaf, gagal mengirim berkas ekspor."
	}
	return ""
}

// searchQuery recognises "/cari <kueri>" and "cari <kueri>" in the bubble.
func searchQuery(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if q, ok := cutPrefixFold(t, "/cari "); ok {
		return q, true
	}
	return cutPrefixFold(t, "cari ")
}

// cutPrefixFold is strings.CutPrefix ignoring ASCII case.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// worklogAPI adapts the worklog service to the agent API (PRD 6.10).
type worklogAPI struct {
	svc *worklog.Service
	rem *worklog.Reminder
}

func (w worklogAPI) Save(ctx context.Context, date, text, clientID string, promptID int64) (api.WorklogSaved, error) {
	r, err := w.svc.SaveFromAgent(ctx, date, text, clientID, promptID)
	var bad *worklog.InputError
	if errors.As(err, &bad) {
		return api.WorklogSaved{}, &api.WorklogInputError{Reason: bad.Reason}
	}
	if err != nil {
		return api.WorklogSaved{}, err
	}
	return api.WorklogSaved{ID: r.ID, Count: r.Count, Entries: r.Entries, Duplicate: r.Duplicate}, nil
}

func (w worklogAPI) Today(ctx context.Context) (api.WorklogToday, error) {
	n, last, workday, err := w.svc.AgentToday(ctx)
	out := api.WorklogToday{Count: n, IsWorkday: workday}
	if !last.IsZero() {
		out.LastAt = &last
	}
	return out, err
}

func (w worklogAPI) Mute(ctx context.Context) error {
	_, err := w.rem.Mute(ctx, "agent")
	return err
}

func (w worklogAPI) Answer(ctx context.Context, promptID int64, action string) error {
	_, err := w.rem.Answer(ctx, promptID, action, "agent")
	if errors.Is(err, worklog.ErrPromptClosed) {
		return api.ErrPromptClosed
	}
	return err
}

// cardReply turns a status card into a Telegram reply.
func cardReply(c status.Card) bot.Reply {
	r := bot.Reply{Text: c.Text}
	for _, row := range c.Rows {
		var out []bot.Button
		for _, b := range row {
			out = append(out, bot.Button{Text: b.Text, Data: b.Data})
		}
		r.Rows = append(r.Rows, out)
	}
	return r
}

var statusViews = map[string]string{"": status.ViewAll, "agen": status.ViewAgent, "agent": status.ViewAgent, "laptop": status.ViewAgent,
	"vps": status.ViewVPS, "server": status.ViewVPS, "llm": status.ViewLLM, "24j": status.View24h, "24h": status.View24h}

// statusReply answers /status [agen|vps|llm|24j].
func statusReply(ctx context.Context, svc *status.Service, args string, log *slog.Logger) bot.Reply {
	view, ok := statusViews[strings.ToLower(strings.TrimSpace(args))]
	if !ok {
		return bot.Reply{Text: "Pakai: /status [agen|vps|llm|24j]"}
	}
	c, err := svc.Report(ctx, view)
	if err != nil {
		log.Error("status failed", "err", err)
		return bot.Reply{Text: "Maaf, status belum bisa diambil. Coba lagi sebentar."}
	}
	return cardReply(c)
}

// appsReply turns an apps card into a Telegram reply.
func appsReply(c apps.Card) bot.Reply {
	r := bot.Reply{Text: c.Text, Toast: c.Toast}
	for _, row := range c.Rows {
		var out []bot.Button
		for _, b := range row {
			out = append(out, bot.Button{Text: b.Text, Data: b.Data})
		}
		r.Rows = append(r.Rows, out)
	}
	return r
}

// eodWorklog lets the end-of-day report ask the worklog about working days
// and add the entries its suggestions stand for.
type eodWorklog struct{ wl *worklog.Service }

func (e eodWorklog) Workday(ctx context.Context, d time.Time) (bool, error) {
	r, err := e.wl.Rules(ctx, d, d.AddDate(0, 0, 1))
	return r.IsWorkday(d), err
}

func (e eodWorklog) Add(ctx context.Context, d time.Time, text string) error {
	e.wl.Save(ctx, d, text, "eod", 0)
	return nil
}

// eodReply turns a report card into a Telegram reply.
func eodReply(c eod.Card) bot.Reply {
	r := bot.Reply{Text: c.Text, Toast: c.Toast}
	for _, row := range c.Rows {
		var out []bot.Button
		for _, b := range row {
			out = append(out, bot.Button{Text: b.Text, Data: b.Data})
		}
		r.Rows = append(r.Rows, out)
	}
	return r
}

// calendarAPI adapts the worklog service to the laptop calendar endpoints.
type calendarAPI struct{ wl *worklog.Service }

func calErr(err error) error {
	var bad *worklog.InputError
	switch {
	case errors.As(err, &bad):
		return &api.WorklogInputError{Reason: bad.Reason}
	case errors.Is(err, worklog.ErrGone):
		return api.ErrCalendarGone
	}
	return err
}

func (c calendarAPI) Month(ctx context.Context, m string) (any, error) {
	t, err := worklog.ParseMonth(m)
	if err != nil {
		return nil, &api.WorklogInputError{Reason: "invalid month"}
	}
	return c.wl.AgentMonth(ctx, t)
}

func (c calendarAPI) Day(ctx context.Context, d string) (any, error) {
	t, err := worklog.ParseDate(d)
	if err != nil {
		return nil, &api.WorklogInputError{Reason: "invalid date"}
	}
	return c.wl.AgentDay(ctx, t)
}

func (c calendarAPI) Edit(ctx context.Context, id int64, text string) (any, error) {
	e, err := c.wl.AgentEdit(ctx, id, text)
	return e, calErr(err)
}

func (c calendarAPI) Delete(ctx context.Context, id int64) error {
	return calErr(c.wl.AgentDelete(ctx, id))
}

// laptopCommand runs a slash command from the laptop's bubble and returns its text.
func laptopCommand(ctx context.Context, cmd, args string, cmds *bot.Commands, st *status.Service, ap *apps.Service,
	wl *worklog.Service, loc *time.Location) (string, bool) {
	const maxText = 3500
	var text string
	switch cmd {
	case "status":
		c, err := st.Report(ctx, status.ViewAll)
		if err != nil {
			return "", false
		}
		text = c.Text
	case "apps":
		c, ok, err := ap.Command(ctx, "apps", args)
		if err != nil || !ok {
			return "", false
		}
		text = c.Text
	case "rekap":
		month := time.Now().In(loc)
		if f := strings.Fields(args); len(f) > 0 {
			t, err := worklog.ParseMonth(f[0])
			if err != nil {
				return "Pakai: /rekap [YYYY-MM]", true
			}
			month = t
		}
		r, err := wl.Rekap(ctx, month)
		if err != nil {
			return "", false
		}
		text = r
	case "today", "tasks", "done", "form":
		text = cmds.Handle(ctx, strings.TrimSpace("/"+cmd+" "+args))
		if strings.HasPrefix(text, "Perintah tidak dikenal") {
			return "", false
		}
	default:
		return "", false
	}
	if r := []rune(text); len(r) > maxText {
		text = string(r[:maxText]) + "…"
	}
	return text, true
}
