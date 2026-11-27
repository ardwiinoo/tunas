package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/secret"
)

// ErrNoToken means no device token is stored yet.
var ErrNoToken = errors.New("no device token stored: create one with `tunas-server device create --name laptop`, then run `tunas-agent.exe --set-token`")

// ValidToken checks the shape of a device token (tnd_ + base64url). The
// error never contains the value.
func ValidToken(t string) error {
	if !strings.HasPrefix(t, "tnd_") || len(t) < 20 || len(t) > 128 {
		return errors.New("that does not look like a device token (tnd_...)")
	}
	for _, r := range t[4:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return errors.New("that does not look like a device token (tnd_...)")
		}
	}
	return nil
}

// LoadDeviceToken returns the device token from the encrypted store. If
// config.yaml still holds a plain device_token (plain), it is first moved to
// the store (checked by reading it back) and then removed from config.yaml.
// Nothing here logs or returns the token.
func LoadDeviceToken(cfgPath, plain string, st secret.Store, log *slog.Logger) (string, error) {
	plain = strings.TrimSpace(plain)
	stored, err := st.Load()
	switch {
	case err == nil:
		// the store wins; a leftover plain copy in config.yaml is removed
		if plain != "" {
			scrub(cfgPath, log)
		}
		return strings.TrimSpace(string(stored)), nil
	case errors.Is(err, secret.ErrNotFound), plain != "":
		// nothing stored (or unreadable, e.g. another Windows account) and a
		// plain token is at hand: store it
		if plain == "" {
			return "", ErrNoToken
		}
		if err := ValidToken(plain); err != nil {
			return "", fmt.Errorf("device_token in config.yaml: %w", err)
		}
		if err := st.Save([]byte(plain)); err != nil {
			return "", fmt.Errorf("could not store the device token encrypted: %w", err)
		}
		back, err := st.Load()
		if err != nil || strings.TrimSpace(string(back)) != plain {
			return "", errors.New("the encrypted device token could not be read back; config.yaml was left as it is")
		}
		scrub(cfgPath, log)
		log.Info("device token moved from config.yaml to the encrypted store")
		return plain, nil
	}
	return "", err
}

func scrub(cfgPath string, log *slog.Logger) {
	if err := config.ScrubDeviceToken(cfgPath); err != nil {
		log.Warn("the plain device_token is still in config.yaml: remove that line by hand", "err", err)
	}
}

// StoreDeviceToken validates and stores a token (--set-token).
func StoreDeviceToken(st secret.Store, token string) error {
	token = strings.TrimSpace(token)
	if err := ValidToken(token); err != nil {
		return err
	}
	return st.Save([]byte(token))
}
