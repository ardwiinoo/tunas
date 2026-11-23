// Package config loads and validates configuration for the server (env vars)
// and the agent (YAML file).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed tz database so Asia/Jakarta works in minimal images
)

// Server holds server configuration loaded from environment variables.
// Never log this struct: it contains secrets.
type Server struct {
	TelegramBotToken   string
	TelegramOwnerID    int64
	DatabaseURL        string
	PublicBaseURL      string
	HTTPAddr           string
	Location           *time.Location
	BifrostBaseURL     string
	BifrostAPIKey      string
	LLMPrimaryModel    string
	LLMFallbackModels  []string
	LLMDailyRequestCap int
	TavilyAPIKey       string
	HolidayAPIBase     string // libur nasional + cuti bersama (Phase 7)
	// GitHub backup of the worklog (Phase 7); off unless token and repo are set.
	GitHubBackupToken  string
	GitHubBackupRepo   string
	GitHubBackupBranch string
	GitHubBackupDir    string
	LogLevel           slog.Level
}

// LLMEnabled reports whether enough LLM settings exist to call Bifrost.
func (s *Server) LLMEnabled() bool {
	return s.BifrostAPIKey != "" && s.LLMPrimaryModel != ""
}

// LoadServer reads server config using getenv (usually os.Getenv).
// Error messages name missing/invalid keys but never include their values.
func LoadServer(getenv func(string) string) (*Server, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var errs []error

	c := &Server{
		TelegramBotToken:   get("TELEGRAM_BOT_TOKEN"),
		DatabaseURL:        get("DATABASE_URL"),
		PublicBaseURL:      get("PUBLIC_BASE_URL"),
		HTTPAddr:           orDefault(get("HTTP_ADDR"), ":8080"),
		BifrostBaseURL:     strings.TrimRight(orDefault(get("BIFROST_BASE_URL"), "https://gateway.ardwiinoo.my.id/v1"), "/"),
		BifrostAPIKey:      get("BIFROST_API_KEY"),
		LLMPrimaryModel:    get("LLM_PRIMARY_MODEL"),
		TavilyAPIKey:       get("TAVILY_API_KEY"),
		HolidayAPIBase:     strings.TrimRight(orDefault(get("HOLIDAY_API_BASE"), "https://tanggalmerah.upset.dev"), "/"),
		GitHubBackupToken:  get("GITHUB_BACKUP_TOKEN"),
		GitHubBackupRepo:   get("GITHUB_BACKUP_REPO"),
		GitHubBackupBranch: orDefault(get("GITHUB_BACKUP_BRANCH"), "main"),
		GitHubBackupDir:    get("GITHUB_BACKUP_DIR"),
	}

	for _, k := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "DATABASE_URL", "PUBLIC_BASE_URL"} {
		if get(k) == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
	}

	if v := get("TELEGRAM_OWNER_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			errs = append(errs, errors.New("TELEGRAM_OWNER_ID must be a positive integer"))
		}
		c.TelegramOwnerID = id
	}

	tz := orDefault(get("TIMEZONE"), "Asia/Jakarta")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE %q is not a valid IANA zone", tz))
	}
	c.Location = loc

	for _, m := range strings.Split(get("LLM_FALLBACK_MODELS"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			c.LLMFallbackModels = append(c.LLMFallbackModels, m)
		}
	}

	c.LLMDailyRequestCap = 30
	if v := get("LLM_DAILY_REQUEST_CAP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, errors.New("LLM_DAILY_REQUEST_CAP must be a non-negative integer"))
		}
		c.LLMDailyRequestCap = n
	}

	lvl, err := ParseLogLevel(get("LOG_LEVEL"))
	if err != nil {
		errs = append(errs, err)
	}
	c.LogLevel = lvl

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseLogLevel parses debug|info|warn|error (empty = info).
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("LOG_LEVEL %q must be debug, info, warn or error", s)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
