package projects

import (
	"strings"
	"testing"
)

func extractor(t *testing.T) *Extractor {
	t.Helper()
	e, err := NewExtractor([]Editor{
		{Process: "Code.exe", Name: "VS Code", Pattern: `^(?P<project>.+?) - Visual Studio Code$`, NoProject: []string{"Visual Studio Code"}},
		// a second editor with its own (synthetic) title shape: "project | Editor"
		{Process: "Atelier.exe", Name: "Atelier", Pattern: `^(?P<project>[^|]+?) \| Atelier$`, NoProject: []string{"Atelier"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestExtractionPerEditor(t *testing.T) {
	e := extractor(t)
	type tc struct {
		name, proc, title string
		want              string
		ok                bool
	}
	for _, c := range []tc{
		{"normal", "Code.exe", "project-A - Visual Studio Code", "project-A", true},
		{"process name any case", "code.EXE", "project-A - Visual Studio Code", "project-A", true},
		{"unsaved marker", "Code.exe", "● project-A - Visual Studio Code", "project-A", true},
		{"extension development host", "Code.exe", "[Extension Development Host] project-A - Visual Studio Code", "project-A", true},
		{"both", "Code.exe", "● [Extension Development Host] project-A - Visual Studio Code", "project-A", true},
		{"no folder open", "Code.exe", "Visual Studio Code", NoProject, true},
		{"remote ssh", "Code.exe", "project-A [SSH: prod-server-01] - Visual Studio Code", "project-A", true},
		{"remote wsl", "Code.exe", "project-A [WSL: Ubuntu] - Visual Studio Code", "project-A", true},
		{"spaces collapsed", "Code.exe", "  my   project  - Visual Studio Code", "my project", true},
		// the default title template puts a file name first: never taken for a project
		{"file name in front", "Code.exe", "main.go - project-A - Visual Studio Code", "", false},
		{"file name only (no folder, a file open)", "Code.exe", "secrets.env - Visual Studio Code", "", false},
		{"file name with path-less extension", "Code.exe", "notes.MD - Visual Studio Code", "", false},
		{"dotted project name is fine", "Code.exe", "tunas.server - Visual Studio Code", "tunas.server", true},
		{"does not match", "Code.exe", "Untitled-1 • Notepad", "", false},
		{"generic start page", "Code.exe", "Welcome - Visual Studio Code", "", false},
		{"settings tab", "Code.exe", "Settings - Visual Studio Code", "", false},
		{"empty", "Code.exe", "", "", false},
		{"only markers", "Code.exe", "● ", "", false},
		{"path", "Code.exe", `C:\Users\me\secret - Visual Studio Code`, "", false},
		{"slash", "Code.exe", "a/b - Visual Studio Code", "", false},
		{"too long", "Code.exe", strings.Repeat("x", 101) + " - Visual Studio Code", "", false},
		{"control char", "Code.exe", "pro\x00ject - Visual Studio Code", "", false},
		{"other editor, own pattern", "Atelier.exe", "project-B | Atelier", "project-B", true},
		{"other editor, no folder", "Atelier.exe", "Atelier", NoProject, true},
		{"other editor, other pattern does not apply", "Atelier.exe", "project-B - Visual Studio Code", "", false},
		{"not an allowed editor", "chrome.exe", "project-A - Visual Studio Code", "", false},
		{"unicode name", "Code.exe", "proyék-ünï - Visual Studio Code", "proyék-ünï", true},
	} {
		got, ok := e.Project(c.proc, c.title)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: Project(%q, %q) = %q, %v; want %q, %v", c.name, c.proc, c.title, got, ok, c.want, c.ok)
		}
	}
}

func TestEditorsAreValidated(t *testing.T) {
	for name, ed := range map[string]Editor{
		"no process":     {Pattern: `(?P<project>.+)`},
		"path":           {Process: `C:\x\Code.exe`, Pattern: `(?P<project>.+)`},
		"bad regexp":     {Process: "Code.exe", Pattern: `(?P<project>`},
		"no named group": {Process: "Code.exe", Pattern: `^(.+) - Code$`},
	} {
		if _, err := NewExtractor([]Editor{ed}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	e := extractor(t)
	if !e.Allowed("code.exe") || e.Allowed("chrome.exe") || e.EditorName("Code.exe") != "VS Code" || e.EditorName("x.exe") != "" {
		t.Error("allowed editors and names")
	}
	if got, _ := NewExtractor(DefaultEditors()); !got.Allowed("Code.exe") || got.Allowed("Antigravity.exe") {
		t.Error("the default covers VS Code only; other editors are added after --probe-title")
	}
}

func TestKeyIsCaseInsensitive(t *testing.T) {
	if Key("  Project-A ") != Key("project-a") || Key("a  b") != "a b" {
		t.Error("Key")
	}
}
