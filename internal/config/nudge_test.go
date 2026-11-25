package config

import (
	"strings"
	"testing"
)

func TestDistractNudgeConfigAndAlias(t *testing.T) {
	base := "server_url: https://x.example\ndevice_token: t\n"
	c, err := ParseAgent([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.Forms.DistractAfter() != 30 || c.Forms.DistractCooldown() != 45 || c.Forms.DistractSnoozeMinutes != 90 || c.Forms.DistractMaxPerDay != 4 {
		t.Fatalf("defaults %+v", c.Forms)
	}
	// the old names still work (an alias) ...
	c, err = ParseAgent([]byte(base + "forms:\n  tegur_after_minutes: 20\n  tegur_cooldown_minutes: 15\n"))
	if err != nil || c.Forms.DistractAfter() != 20 || c.Forms.DistractCooldown() != 15 {
		t.Fatalf("alias %+v %v", c.Forms, err)
	}
	// ... and the new names win
	c, err = ParseAgent([]byte(base + "forms:\n  tegur_after_minutes: 20\n  distract_after_minutes: 40\n  distract_cooldown_minutes: 60\n  distract_snooze_minutes: 120\n  distract_max_per_day: 2\n"))
	if err != nil || c.Forms.DistractAfter() != 40 || c.Forms.DistractCooldown() != 60 || c.Forms.DistractSnoozeMinutes != 120 || c.Forms.DistractMaxPerDay != 2 {
		t.Fatalf("new names %+v %v", c.Forms, err)
	}
	for _, bad := range []string{"distract_after_minutes: -1", "distract_snooze_minutes: 0", "distract_max_per_day: 0"} {
		if _, err := ParseAgent([]byte(base + "forms:\n  " + bad + "\n")); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestExtensionConfig(t *testing.T) {
	base := "server_url: https://x.example\ndevice_token: t\n"
	c, err := ParseAgent([]byte(base))
	if err != nil || c.Extension.Port != 47821 || len(c.Extension.IDs) != 1 || c.Extension.IDs[0] != DefaultExtensionID || len(c.Extension.Browsers) < 2 {
		t.Fatalf("defaults %+v %v", c.Extension, err)
	}
	for name, yml := range map[string]string{
		"port too low":     "extension:\n  port: 80\n",
		"port too high":    "extension:\n  port: 70000\n",
		"id with a path":   "extension:\n  ids: [\"chrome-extension://abc\"]\n",
		"id too short":     "extension:\n  ids: [abcdef]\n",
		"id with capitals": "extension:\n  ids: [ICAGOAKBNKJJCLFOABCPDBBDFIMNOJEC]\n",
	} {
		if _, err := ParseAgent([]byte(base + yml)); err == nil || strings.Contains(err.Error(), "device_token") {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}
