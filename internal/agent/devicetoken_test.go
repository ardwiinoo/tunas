package agent

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/secret"
)

const tok = "tnd_AbCdEfGh-ijklMNOP_0123456789xyz"

type fakeP struct{}

func (fakeP) Protect(p []byte) ([]byte, error) {
	out := []byte("ENC:")
	for _, b := range p {
		out = append(out, b+1)
	}
	return out, nil
}
func (fakeP) Unprotect(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, []byte("ENC:")) {
		return nil, errors.New("bad blob")
	}
	var out []byte
	for _, c := range b[4:] {
		out = append(out, c-1)
	}
	return out, nil
}

func rig(t *testing.T, yaml string) (cfg string, st secret.Store, logs *bytes.Buffer, log *slog.Logger) {
	dir := t.TempDir()
	cfg = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	logs = &bytes.Buffer{}
	log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return cfg, secret.Store{Path: filepath.Join(dir, "token.bin"), P: fakeP{}}, logs, log
}

// A plain token in config.yaml is stored encrypted, removed from the file
// (the rest of the file stays), and never reaches the log.
func TestPlainTokenIsMigrated(t *testing.T) {
	yaml := "server_url: \"https://tunas.example.com\"\r\ndevice_token: \"" + tok + "\"\r\nsample_interval_seconds: 10\r\n"
	cfg, st, logs, log := rig(t, yaml)
	got, err := LoadDeviceToken(cfg, tok, st, log)
	if err != nil || got != tok {
		t.Fatalf("%q, %v", got, err)
	}
	b, _ := os.ReadFile(cfg)
	if strings.Contains(string(b), tok) {
		t.Fatal("the token is still in config.yaml")
	}
	if !strings.Contains(string(b), "server_url: \"https://tunas.example.com\"\r\n") || !strings.Contains(string(b), "sample_interval_seconds: 10\r\n") {
		t.Fatalf("the rest of config.yaml changed:\n%q", b)
	}
	if _, err := config.ParseAgent(b); err != nil {
		t.Fatalf("config.yaml is no longer valid: %v", err)
	}
	raw, _ := os.ReadFile(st.Path)
	if bytes.Contains(raw, []byte(tok)) {
		t.Fatal("the token is plain in token.bin")
	}
	if strings.Contains(logs.String(), tok) || strings.Contains(logs.String(), "AbCdEfGh") {
		t.Fatalf("the token is in the log:\n%s", logs)
	}
	// next start: token comes from the store, config has none
	got, err = LoadDeviceToken(cfg, "", st, log)
	if err != nil || got != tok {
		t.Fatalf("second start: %q, %v", got, err)
	}
}

func TestStoreWinsAndLeftoverIsScrubbed(t *testing.T) {
	cfg, st, _, log := rig(t, "device_token: \"tnd_old-plain-token-0000000000\"\n")
	if err := StoreDeviceToken(st, tok); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDeviceToken(cfg, "tnd_old-plain-token-0000000000", st, log)
	if err != nil || got != tok {
		t.Fatalf("%q, %v", got, err)
	}
	if b, _ := os.ReadFile(cfg); strings.Contains(string(b), "old-plain") {
		t.Fatal("the leftover plain token was not removed")
	}
}

func TestNoTokenAnywhere(t *testing.T) {
	cfg, st, _, log := rig(t, "server_url: \"https://x.example.com\"\n")
	if _, err := LoadDeviceToken(cfg, "", st, log); !errors.Is(err, ErrNoToken) {
		t.Fatalf("got %v", err)
	}
}

func TestUnreadableStoreWithoutPlainFails(t *testing.T) {
	cfg, st, _, log := rig(t, "x: 1\n")
	_ = os.WriteFile(st.Path, []byte("not ours"), 0o600)
	if _, err := LoadDeviceToken(cfg, "", st, log); err == nil || errors.Is(err, ErrNoToken) {
		t.Fatalf("got %v", err)
	}
}

func TestBadTokensAreRejectedWithoutEchoing(t *testing.T) {
	_, st, _, _ := rig(t, "x: 1\n")
	for _, bad := range []string{"", "abc", "tnd_", "tnd_has space in it 0123456789", "xyz_AbCdEfGh0123456789012345", "tnd_" + strings.Repeat("a", 200)} {
		err := StoreDeviceToken(st, bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if len(bad) > 6 && strings.Contains(err.Error(), bad) {
			t.Errorf("the error repeats the token: %v", err)
		}
	}
	if _, err := os.Stat(st.Path); err == nil {
		t.Fatal("a bad token was stored")
	}
}

// A config file whose token line cannot be found stays untouched, and the
// failure is only a warning (the agent still runs).
func TestScrubFailureIsOnlyAWarning(t *testing.T) {
	cfg, st, logs, log := rig(t, "extra:\n  device_token: \""+tok+"\"\n")
	got, err := LoadDeviceToken(cfg, tok, st, log)
	if err != nil || got != tok {
		t.Fatalf("%q, %v", got, err)
	}
	if !strings.Contains(logs.String(), "remove that line by hand") || strings.Contains(logs.String(), tok) {
		t.Fatalf("log: %s", logs)
	}
}
