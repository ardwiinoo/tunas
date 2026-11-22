package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoWindowTitleAPIs guards PRD 8.3: the agent must never read window titles.
func TestNoWindowTitleAPIs(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	files2, _ := filepath.Glob("../../cmd/agent/*.go")
	for _, f := range append(files, files2...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"GetWindowText", "GetWindowTextLength", "WM_GETTEXT", "store_titles: true"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s uses %s", f, banned)
			}
		}
	}
}
