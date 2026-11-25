// Package projects extracts the name of the project an editor window is
// about (PRD Fase 8f). The window title is read only in memory, matched
// against the owner's pattern for that editor and thrown away: only the
// extracted name leaves this package. Titles never reach a log, a database,
// the sync payload or an LLM.
package projects

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NoProject is the name recorded when an editor has no folder open.
const NoProject = "(tanpa proyek)"

// MaxName is the longest project name (matches the database check).
const MaxName = 100

// Editor is one allowed editor process and how to read its title.
type Editor struct {
	Process   string   `yaml:"process"`    // executable name, e.g. "Code.exe"
	Name      string   `yaml:"name"`       // display name, e.g. "VS Code" (default: the process)
	Pattern   string   `yaml:"pattern"`    // must contain the named group (?P<project>...)
	NoProject []string `yaml:"no_project"` // whole titles that mean "no folder open"
}

// DefaultEditors is only a starting point: the pattern for VS Code assumes
// the recommended title setting "${rootName}${separator}${appName}". Titles
// with more segments (a file name in front) do not match and are ignored, so
// a file name is never taken for a project. Other editors (Antigravity, ...)
// must be added by the owner after checking their real titles with
// `tunas-agent.exe --probe-title`.
func DefaultEditors() []Editor {
	return []Editor{{
		Process: "Code.exe", Name: "VS Code",
		Pattern:   `^(?P<project>.+?) - Visual Studio Code$`,
		NoProject: []string{"Visual Studio Code"},
	}}
}

type compiled struct {
	editor Editor
	re     *regexp.Regexp
	idx    int
	none   map[string]bool
}

// Extractor turns (process, title) into a project name.
type Extractor struct{ byProc map[string]*compiled }

// NewExtractor validates and compiles the editors.
func NewExtractor(eds []Editor) (*Extractor, error) {
	e := &Extractor{byProc: map[string]*compiled{}}
	var errs []error
	for _, ed := range eds {
		key := strings.ToLower(strings.TrimSpace(ed.Process))
		if key == "" || strings.ContainsAny(key, `/\`) {
			errs = append(errs, fmt.Errorf("projects.editors: bad process %q", ed.Process))
			continue
		}
		re, err := regexp.Compile(ed.Pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("projects.editors %s: pattern: %w", ed.Process, err))
			continue
		}
		idx := re.SubexpIndex("project")
		if idx < 0 {
			errs = append(errs, fmt.Errorf("projects.editors %s: pattern needs a (?P<project>...) group", ed.Process))
			continue
		}
		c := &compiled{editor: ed, re: re, idx: idx, none: map[string]bool{}}
		for _, t := range ed.NoProject {
			c.none[strings.TrimSpace(t)] = true
		}
		e.byProc[key] = c
	}
	return e, errors.Join(errs...)
}

// Allowed reports whether a process is one of the configured editors.
func (e *Extractor) Allowed(process string) bool {
	_, ok := e.byProc[strings.ToLower(process)]
	return ok
}

// EditorName is the display name of an allowed editor ("" if not allowed).
func (e *Extractor) EditorName(process string) string {
	c, ok := e.byProc[strings.ToLower(process)]
	if !ok {
		return ""
	}
	if c.editor.Name != "" {
		return c.editor.Name
	}
	return c.editor.Process
}

// genericNames are tabs that are not projects (an editor with no folder
// showing its start page, settings, ...).
var genericNames = map[string]bool{"welcome": true, "get started": true, "settings": true, "extensions": true, "untitled": true}

// reRemote matches a trailing remote marker such as "[SSH: host]" or "[WSL: Ubuntu]".
var reRemote = regexp.MustCompile(`\s*\[[^\]]*\]\s*$`)

// Project extracts the project name from a title. ok=false means: do not
// record anything (not an allowed editor, or the title does not match the
// pattern). The title is not retained.
func (e *Extractor) Project(process, title string) (string, bool) {
	c, ok := e.byProc[strings.ToLower(process)]
	if !ok {
		return "", false
	}
	t := strings.TrimSpace(title)
	// marker for unsaved changes and the extension development host prefix
	t = strings.TrimSpace(strings.TrimLeft(t, "●•* "))
	t = strings.TrimSpace(strings.TrimPrefix(t, "[Extension Development Host]"))
	if t == "" {
		return "", false
	}
	if c.none[t] {
		return NoProject, true
	}
	m := c.re.FindStringSubmatch(t)
	if m == nil {
		return "", false
	}
	name := m[c.idx]
	// a remote marker names a host; keep only the project
	for reRemote.MatchString(name) {
		name = reRemote.ReplaceAllString(name, "")
	}
	if strings.Contains(name, " - ") { // more segments: a file name in front of the project
		return "", false
	}
	name, ok = Clean(name)
	if !ok || genericNames[strings.ToLower(name)] || looksLikeFile(name) {
		return "", false
	}
	return name, true
}

// fileExts are extensions of files an editor opens. A "project" ending in
// one of them is a file name (an editor without a folder shows the file), and
// file names are never recorded. (So a project literally named "next.js" is
// not recorded either.)
var fileExts = map[string]bool{
	"go": true, "py": true, "js": true, "ts": true, "tsx": true, "jsx": true, "json": true, "md": true, "txt": true, "env": true,
	"yaml": true, "yml": true, "sql": true, "html": true, "css": true, "java": true, "cs": true, "cpp": true, "c": true, "h": true,
	"rs": true, "php": true, "rb": true, "sh": true, "ps1": true, "xml": true, "toml": true, "ini": true, "log": true, "csv": true,
	"docx": true, "xlsx": true, "pdf": true, "vue": true, "svelte": true, "kt": true, "swift": true, "lock": true, "mod": true,
}

func looksLikeFile(name string) bool {
	i := strings.LastIndexByte(name, '.')
	return i > 0 && fileExts[strings.ToLower(name[i+1:])]
}

// Clean validates and normalizes a project name: trimmed, single spaces,
// at most MaxName characters, no control characters and nothing that looks
// like a path.
func Clean(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || utf8.RuneCountInString(s) > MaxName || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return "", false
		}
	}
	if len(s) >= 2 && s[1] == ':' { // "C:..."
		return "", false
	}
	return s, true
}

// Key is the case-insensitive identity of a project (aliases and the
// blocklist match on it).
func Key(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }
