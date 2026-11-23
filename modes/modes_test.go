package modes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/forms"
)

func TestLoadAllForms(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range forms.All {
		m, ok := ms[string(f)]
		if !ok || !strings.Contains(m.Voice, "Tunas") {
			t.Errorf("mode %s missing or invalid: %+v", f, m)
		}
	}
}

func TestMikirLinesAndOverride(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	m := ms["mikir"]
	for _, phase := range []string{"mikir", "mikir_lama", "sangat_lama"} {
		if len(m.Lines[phase]) == 0 {
			t.Fatalf("mikir.yaml lacks lines.%s", phase)
		}
	}
	if forms.Valid(forms.Mikir) {
		t.Fatal("mikir is display-only, not settable with /form")
	}

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("mikir.yaml", "lines:\n  mikir:\n    - \"sebentar ya, kawan\"\n")
	write("rusak.yaml", "id: [tidak valid\n")
	write("panjang.yaml", "id: mikir\nlines:\n  mikir_lama:\n    - \""+strings.Repeat("a", MaxLineLen+1)+"\"\n")
	got, warns, err := LoadWithOverrides(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 2 {
		t.Fatalf("want warnings for the broken and the too-long file, got %v", warns)
	}
	g := got["mikir"]
	if len(g.Lines["mikir"]) != 1 || g.Lines["mikir"][0] != "sebentar ya, kawan" {
		t.Fatalf("override not applied: %v", g.Lines["mikir"])
	}
	if len(g.Lines["mikir_lama"]) != len(m.Lines["mikir_lama"]) || g.Voice != m.Voice {
		t.Fatal("phases and fields not in the override must keep the embedded values")
	}
	if _, _, err := LoadWithOverrides(filepath.Join(dir, "tidak-ada")); err != nil {
		t.Fatal("a missing override dir is fine")
	}
}
