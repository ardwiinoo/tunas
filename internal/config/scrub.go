package config

import (
	"errors"
	"os"
	"regexp"

	"github.com/ardwiinoo/tunas/internal/secret"
)

// ScrubNote replaces the device_token line once the token is stored encrypted.
const ScrubNote = "# device_token: disimpan terenkripsi (Windows DPAPI) di token.bin; ganti dengan: tunas-agent.exe --set-token"

var reTokenLine = regexp.MustCompile(`(?m)^device_token:[^\r\n]*`)

// ScrubDeviceToken removes the plain device_token value from the agent's
// config file (atomic write, line endings and the rest of the file kept).
func ScrubDeviceToken(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !reTokenLine.Match(b) {
		return errors.New("no top-level device_token line found in the config file")
	}
	out := reTokenLine.ReplaceAllLiteral(b, []byte(ScrubNote))
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	return secret.WriteFileAtomic(path, out, mode)
}
