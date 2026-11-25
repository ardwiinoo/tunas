package config

import (
	"strings"
	"testing"
)

func TestDefaultAppSeed(t *testing.T) {
	seed := DefaultAppSeed()
	for app, cat := range seed {
		if app != strings.ToLower(app) || !validCategories[cat] || !strings.HasSuffix(app, ".exe") {
			t.Errorf("bad seed entry %q → %q", app, cat)
		}
	}
	for app, want := range map[string]string{"code.exe": "work", "windowsterminal.exe": "work", "cursor.exe": "work", "chrome.exe": "browse", "discord.exe": "distract"} {
		if seed[app] != want {
			t.Errorf("%s = %q, want %q", app, seed[app], want)
		}
	}
	// the owner's IDE is verified from real data through /apps, never guessed
	if _, ok := seed["antigravity.exe"]; ok {
		t.Error("antigravity.exe must not be guessed")
	}
}
