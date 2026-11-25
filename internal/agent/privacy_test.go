package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// titleAPIFile is the only file allowed to call the window title API: the
// editor project reader of Fase 8f (PRD 8.3, second exception).
const titleAPIFile = "internal/projects/title_windows.go"

// TestNoWindowTitleAPIs guards PRD 8.3: the window title is never read,
// except in the one file that reads it in memory for allowed editors. It
// scans every Go file of the repository, not just this package.
func TestNoWindowTitleAPIs(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []string{"GetWindowText", "GetWindowTextLength", "WM_GETTEXT", "InternalGetWindowText", "store_titles: true"}
	scanned, found := 0, false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "docs", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, s := range banned {
			if !strings.Contains(string(b), s) {
				continue
			}
			if rel == titleAPIFile && s != "store_titles: true" {
				found = true
				continue
			}
			t.Errorf("%s uses %s (only %s may read window titles)", rel, s, titleAPIFile)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 || !found {
		t.Fatalf("scanned %d files, title reader found=%v: the guard is not looking where it should", scanned, found)
	}
}
