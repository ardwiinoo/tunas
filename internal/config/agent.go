package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ardwiinoo/tunas/internal/projects"
)

// Agent is the laptop agent configuration (%APPDATA%\tunas\config.yaml).
type Agent struct {
	ServerURL             string              `yaml:"server_url"`
	DeviceToken           string              `yaml:"device_token"`
	SampleIntervalSeconds int                 `yaml:"sample_interval_seconds"`
	FlushIntervalSeconds  int                 `yaml:"flush_interval_seconds"`
	IdleThresholdSeconds  int                 `yaml:"idle_threshold_seconds"`
	StoreTitles           bool                `yaml:"store_titles"`
	AppCategories         map[string][]string `yaml:"app_categories"`
	Forms                 AgentForms          `yaml:"forms"`
	Overlay               AgentOverlay        `yaml:"overlay"`
	Loading               AgentLoading        `yaml:"loading"`
	Animations            bool                `yaml:"animations"` // false: static poses, no typing effect
	Bubble                AgentBubble         `yaml:"bubble"`
	WorklogBubbleInFocus  bool                `yaml:"worklog_bubble_in_focus"` // worklog bubbles also in form fokus
	Speech                AgentSpeech         `yaml:"speech"`
	Projects              AgentProjects       `yaml:"projects"`
	Extension             AgentExtension      `yaml:"extension"`
	TLS                   AgentTLS            `yaml:"tls"`
}

// DefaultExtensionID is the id of the extension in this repository's
// extension/ folder (its manifest carries a fixed key, so the id does not
// depend on where it is loaded from).
const DefaultExtensionID = "icagoakbnkjjclfoabcpdbbdfimnojec"

// AgentExtension configures the local bridge for the browser extension
// (Fase 8c). The bridge only runs while "Catat domain situs" is on.
type AgentExtension struct {
	Port     int      `yaml:"port"`     // 127.0.0.1:<port>
	IDs      []string `yaml:"ids"`      // extension ids allowed to call the bridge
	Browsers []string `yaml:"browsers"` // processes whose active tab counts
}

// AgentProjects lists the editors whose window title may be read (in memory
// only) to find the project name. Recording itself is off until the owner
// turns it on in the tray (Fase 8f).
type AgentProjects struct {
	Editors []projects.Editor `yaml:"editors"`
}

// AgentSpeech sets the "bicara" gesture: an answer is typed out over
// len(text) × ms_per_char, kept between min_seconds and max_seconds.
type AgentSpeech struct {
	MsPerChar  int     `yaml:"ms_per_char"`
	MinSeconds float64 `yaml:"min_seconds"`
	MaxSeconds float64 `yaml:"max_seconds"`
}

// AgentBubble configures the chat bubble.
type AgentBubble struct {
	IdleCloseSeconds int `yaml:"idle_close_seconds"` // close an open bubble after this long without activity
}

// AgentLoading sets when the bubble's waiting texts change.
type AgentLoading struct {
	SlowAfterSeconds     int `yaml:"slow_after_seconds"`      // "mikir" → "mikir lama"
	VerySlowAfterSeconds int `yaml:"very_slow_after_seconds"` // → "sangat lama" (Esc to cancel)
}

// AgentForms holds form state-machine thresholds.
type AgentForms struct {
	TegurAfterMinutes      int `yaml:"tegur_after_minutes"`
	TegurCooldownMinutes   int `yaml:"tegur_cooldown_minutes"`
	TidurAfterIdleMinutes  int `yaml:"tidur_after_idle_minutes"`
	SenangSeconds          int `yaml:"senang_seconds"`
	TaskDoneWindowSeconds  int `yaml:"task_done_window_seconds"`
	IdleMotionMinMinutes   int `yaml:"idle_motion_min_minutes"`
	IdleMotionMaxMinutes   int `yaml:"idle_motion_max_minutes"`
	ReminderDisplaySeconds int `yaml:"reminder_display_seconds"`
	TegurDisplaySeconds    int `yaml:"tegur_display_seconds"` // unused since the nudge asks a question; kept so old files load

	// The distraction nudge (Fase 8c). 0 for after/cooldown = use the old
	// tegur_after_minutes / tegur_cooldown_minutes (an alias).
	DistractAfterMinutes    int `yaml:"distract_after_minutes"`
	DistractCooldownMinutes int `yaml:"distract_cooldown_minutes"`
	DistractSnoozeMinutes   int `yaml:"distract_snooze_minutes"`
	DistractMaxPerDay       int `yaml:"distract_max_per_day"`
}

// AgentOverlay configures the on-screen character (Phase 4).
type AgentOverlay struct {
	Enabled bool   `yaml:"enabled"`
	Corner  string `yaml:"corner"` // bottom-right | bottom-left | top-right | top-left
	Monitor int    `yaml:"monitor"`
	MarginX int    `yaml:"margin_x"`
	MarginY int    `yaml:"margin_y"`
}

// Valid categories for app_categories.
var validCategories = map[string]bool{"work": true, "browse": true, "distract": true, "other": true}

// DefaultAgent returns the default agent config.
func DefaultAgent() *Agent {
	return &Agent{
		ServerURL:             "https://tunas.ardwiinoo.my.id",
		SampleIntervalSeconds: 10,
		FlushIntervalSeconds:  60,
		IdleThresholdSeconds:  120,
		AppCategories: map[string][]string{
			"work":     {"Code.exe", "WindowsTerminal.exe", "idea64.exe", "GitHubDesktop.exe"},
			"browse":   {"chrome.exe", "msedge.exe", "firefox.exe"},
			"distract": {"Discord.exe", "Spotify.exe"},
		},
		Forms: AgentForms{
			TegurAfterMinutes:      30,
			TegurCooldownMinutes:   45,
			TidurAfterIdleMinutes:  10,
			SenangSeconds:          5,
			TaskDoneWindowSeconds:  10,
			IdleMotionMinMinutes:   3,
			IdleMotionMaxMinutes:   7,
			ReminderDisplaySeconds: 15,
			TegurDisplaySeconds:    10,
			DistractSnoozeMinutes:  90,
			DistractMaxPerDay:      4,
		},
		Overlay:    AgentOverlay{Enabled: true, Corner: "bottom-right", MarginX: 24, MarginY: 24},
		Loading:    AgentLoading{SlowAfterSeconds: 3, VerySlowAfterSeconds: 8},
		Animations: true,
		Bubble:     AgentBubble{IdleCloseSeconds: 60},
		Speech:     AgentSpeech{MsPerChar: 40, MinSeconds: 2, MaxSeconds: 8},
		Projects:   AgentProjects{Editors: projects.DefaultEditors()},
		Extension: AgentExtension{Port: 47821, IDs: []string{DefaultExtensionID},
			Browsers: []string{"chrome.exe", "msedge.exe", "brave.exe", "vivaldi.exe", "opera.exe"}},
	}
}

// LoadAgentFile reads and validates an agent YAML file. Missing fields keep defaults.
func LoadAgentFile(path string) (*Agent, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseAgent(b)
}

// ParseAgent parses agent YAML on top of defaults and validates it.
func ParseAgent(b []byte) (*Agent, error) {
	c := DefaultAgent()
	defCats := c.AppCategories
	c.AppCategories = nil // user mapping replaces (not merges) the default
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(c.AppCategories) == 0 {
		c.AppCategories = defCats
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks value ranges. Errors never include the device token.
func (c *Agent) Validate() error {
	var errs []error
	if err := ValidateServerURL(c.ServerURL); err != nil {
		errs = append(errs, err)
	}
	if _, err := projects.NewExtractor(c.Projects.Editors); err != nil {
		errs = append(errs, err)
	}
	if p := c.Extension.Port; p < 1024 || p > 65535 {
		errs = append(errs, errors.New("extension.port must be 1024..65535"))
	}
	for _, id := range c.Extension.IDs {
		if len(id) != 32 || strings.Trim(id, "abcdefghijklmnop") != "" {
			errs = append(errs, errors.New("extension.ids: an id is 32 letters a-p"))
		}
	}
	if c.StoreTitles {
		errs = append(errs, errors.New("store_titles must be false: window titles are never collected"))
	}
	pos := func(name string, v int) {
		if v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be > 0", name))
		}
	}
	pos("sample_interval_seconds", c.SampleIntervalSeconds)
	pos("flush_interval_seconds", c.FlushIntervalSeconds)
	pos("idle_threshold_seconds", c.IdleThresholdSeconds)
	pos("forms.tegur_after_minutes", c.Forms.TegurAfterMinutes)
	pos("forms.tidur_after_idle_minutes", c.Forms.TidurAfterIdleMinutes)
	pos("forms.senang_seconds", c.Forms.SenangSeconds)
	pos("forms.idle_motion_min_minutes", c.Forms.IdleMotionMinMinutes)
	for name, v := range map[string]int{"forms.distract_after_minutes": c.Forms.DistractAfterMinutes, "forms.distract_cooldown_minutes": c.Forms.DistractCooldownMinutes} {
		if v < 0 {
			errs = append(errs, fmt.Errorf("%s must be >= 0", name))
		}
	}
	pos("forms.distract_snooze_minutes", c.Forms.DistractSnoozeMinutes)
	pos("forms.distract_max_per_day", c.Forms.DistractMaxPerDay)
	if c.Forms.TegurCooldownMinutes < 0 {
		errs = append(errs, errors.New("forms.tegur_cooldown_minutes must be >= 0"))
	}
	if c.Forms.IdleMotionMaxMinutes < c.Forms.IdleMotionMinMinutes {
		errs = append(errs, errors.New("forms.idle_motion_max_minutes must be >= idle_motion_min_minutes"))
	}
	for cat := range c.AppCategories {
		if !validCategories[cat] {
			errs = append(errs, fmt.Errorf("app_categories: unknown category %q", cat))
		}
	}
	switch c.Overlay.Corner {
	case "bottom-right", "bottom-left", "top-right", "top-left":
	default:
		errs = append(errs, fmt.Errorf("overlay.corner %q invalid", c.Overlay.Corner))
	}
	if l := c.Loading; l.SlowAfterSeconds < 1 || l.VerySlowAfterSeconds <= l.SlowAfterSeconds || l.VerySlowAfterSeconds > 30 {
		errs = append(errs, errors.New("loading: need 1 <= slow_after_seconds < very_slow_after_seconds <= 30"))
	}
	if sp := c.Speech; sp.MsPerChar < 5 || sp.MsPerChar > 500 || sp.MinSeconds <= 0 || sp.MaxSeconds < sp.MinSeconds || sp.MaxSeconds > 30 {
		errs = append(errs, errors.New("speech: need 5 <= ms_per_char <= 500 and 0 < min_seconds <= max_seconds <= 30"))
	}
	if n := c.Bubble.IdleCloseSeconds; n < 10 || n > 600 {
		errs = append(errs, errors.New("bubble.idle_close_seconds must be 10..600"))
	}
	return errors.Join(errs...)
}

// DefaultAppSeed is the starting app → category mapping the server stores
// (lower-case names). It covers common editors and terminals; IDEs it does
// not know show up as "?" in /apps and are categorized from Telegram. Names
// of new IDEs are deliberately not guessed.
func DefaultAppSeed() map[string]string {
	out := map[string]string{}
	for cat, apps := range DefaultAgent().AppCategories {
		for _, a := range apps {
			out[strings.ToLower(a)] = cat
		}
	}
	for _, a := range []string{
		"cursor.exe", "windsurf.exe", "code - insiders.exe", "sublime_text.exe", "notepad++.exe", "devenv.exe",
		"rider64.exe", "webstorm64.exe", "pycharm64.exe", "goland64.exe", "clion64.exe", "datagrip64.exe", "studio64.exe",
		"wt.exe", "powershell.exe", "pwsh.exe", "cmd.exe", "mintty.exe", "postman.exe", "dbeaver.exe", "tableplus.exe",
		"githubdesktop.exe", "sourcetree.exe", "docker desktop.exe",
	} {
		out[a] = "work"
	}
	for _, a := range []string{"brave.exe", "opera.exe", "vivaldi.exe", "arc.exe"} {
		out[a] = "browse"
	}
	for _, a := range []string{"discord.exe", "spotify.exe", "steam.exe", "vlc.exe"} {
		out[a] = "distract"
	}
	return out
}

// CategoryOf maps an executable name to its category (case-insensitive), "other" if unmapped.
func (c *Agent) CategoryOf(exe string) string {
	for cat, apps := range c.AppCategories {
		for _, a := range apps {
			if strings.EqualFold(a, exe) {
				return cat
			}
		}
	}
	return "other"
}

// DistractAfter is the distract time in a row before a nudge, in minutes
// (distract_after_minutes, or the old tegur_after_minutes).
func (f AgentForms) DistractAfter() int {
	if f.DistractAfterMinutes > 0 {
		return f.DistractAfterMinutes
	}
	return f.TegurAfterMinutes
}

// DistractCooldown is the time between two nudges, in minutes.
func (f AgentForms) DistractCooldown() int {
	if f.DistractCooldownMinutes > 0 {
		return f.DistractCooldownMinutes
	}
	return f.TegurCooldownMinutes
}

// AgentTLS holds the optional mTLS client certificate (Fase 9c). Both empty:
// no client certificate is sent. ClientKey empty with ClientCert set: the key
// comes from the DPAPI-protected store (tunas-agent.exe --set-client-key).
type AgentTLS struct {
	ClientCert string `yaml:"client_cert"` // PEM file
	ClientKey  string `yaml:"client_key"`  // PEM file (optional, see above)
}
