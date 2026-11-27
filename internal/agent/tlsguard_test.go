package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoInsecureSkipVerify guards PRD 9a: no code in the repository turns off
// certificate verification. Like TestNoWindowTitleAPIs it scans every Go file
// outside tests.
func TestNoInsecureSkipVerify(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := 0
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
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		if strings.Contains(string(b), "InsecureSkipVerify") {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s turns certificate verification off", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files: the guard is not looking where it should", scanned)
	}
}
