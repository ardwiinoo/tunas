package config

import (
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:secret-token-value",
		"TELEGRAM_OWNER_ID":  "42",
		"DATABASE_URL":       "postgresql://u:supersecretpw@localhost/tunas",
		"PUBLIC_BASE_URL":    "https://example.test",
	}
}

func TestLoadServerDefaults(t *testing.T) {
	c, err := LoadServer(envMap(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.Location.String() != "Asia/Jakarta" || c.LLMDailyRequestCap != 30 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.TelegramOwnerID != 42 {
		t.Fatalf("owner id = %d", c.TelegramOwnerID)
	}
	if c.LLMEnabled() {
		t.Fatal("LLM should be disabled without key/model")
	}
}

func TestLoadServerMissingRequiredDoesNotLeakValues(t *testing.T) {
	env := validEnv()
	delete(env, "TELEGRAM_BOT_TOKEN")
	env["TELEGRAM_OWNER_ID"] = "abc"
	_, err := LoadServer(envMap(env))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"TELEGRAM_BOT_TOKEN is required", "TELEGRAM_OWNER_ID must be"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "supersecretpw") || strings.Contains(msg, "abc") {
		t.Errorf("error leaks values: %q", msg)
	}
}

func TestLoadServerFallbackModels(t *testing.T) {
	env := validEnv()
	env["LLM_FALLBACK_MODELS"] = " a/b , ,c/d"
	env["BIFROST_API_KEY"] = "k"
	env["LLM_PRIMARY_MODEL"] = "x/y"
	c, err := LoadServer(envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.LLMFallbackModels) != 2 || c.LLMFallbackModels[1] != "c/d" || !c.LLMEnabled() {
		t.Fatalf("got %v", c.LLMFallbackModels)
	}
}

func TestParseAgent(t *testing.T) {
	c, err := ParseAgent([]byte(`
server_url: https://tunas.example
device_token: abc
app_categories:
  work: [Code.exe]
forms:
  tegur_after_minutes: 20
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.SampleIntervalSeconds != 10 || c.Forms.TegurAfterMinutes != 20 || c.Forms.TegurCooldownMinutes != 45 {
		t.Fatalf("defaults not merged: %+v", c)
	}
	if c.CategoryOf("code.EXE") != "work" || c.CategoryOf("chrome.exe") != "other" {
		t.Fatal("user categories should replace defaults")
	}
	if sp := c.Speech; sp.MsPerChar != 40 || sp.MinSeconds != 2 || sp.MaxSeconds != 8 {
		t.Fatalf("speech defaults: %+v", sp)
	}
	if c2, err := ParseAgent([]byte("speech: {ms_per_char: 25, min_seconds: 1.5, max_seconds: 6}")); err != nil ||
		c2.Speech.MsPerChar != 25 || c2.Speech.MinSeconds != 1.5 || c2.Speech.MaxSeconds != 6 {
		t.Fatalf("speech from yaml: %+v %v", c2.Speech, err)
	}
	if c.Loading.SlowAfterSeconds != 3 || c.Loading.VerySlowAfterSeconds != 8 || !c.Animations {
		t.Fatalf("loading/animations defaults: %+v %v", c.Loading, c.Animations)
	}
	c, err = ParseAgent([]byte("loading: {slow_after_seconds: 2, very_slow_after_seconds: 6}\nanimations: false\n"))
	if err != nil || c.Loading.SlowAfterSeconds != 2 || c.Loading.VerySlowAfterSeconds != 6 || c.Animations {
		t.Fatalf("loading/animations from yaml: %+v %v %v", c.Loading, c.Animations, err)
	}
}

func TestParseAgentRejects(t *testing.T) {
	cases := map[string]string{
		"titles":   "store_titles: true",
		"badcat":   "app_categories: {games: [x.exe]}",
		"badurl":   "server_url: ftp://x",
		"interval": "sample_interval_seconds: 0",
		"loading":  "loading: {slow_after_seconds: 8, very_slow_after_seconds: 3}",
		"loading0": "loading: {slow_after_seconds: 0}",
		"bubble":   "bubble: {idle_close_seconds: 5}",
		"speech1":  "speech: {ms_per_char: 1}",
		"speech2":  "speech: {min_seconds: 9, max_seconds: 3}",
		"speech3":  "speech: {max_seconds: 99}",
	}
	for name, y := range cases {
		if _, err := ParseAgent([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
