// Package modes embeds the per-form personas (modes/*.yaml).
package modes

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var files embed.FS

// Mode is a form persona.
type Mode struct {
	ID     string              `yaml:"id"`
	Name   string              `yaml:"name"`
	Voice  string              `yaml:"voice"`
	Sprite string              `yaml:"sprite"`
	Lines  map[string][]string `yaml:"lines"` // short texts by phase (mikir.yaml)
}

// MaxLineLen bounds a line (runes) so it fits the bubble.
const MaxLineLen = 60

// Load parses all embedded modes keyed by id.
func Load() (map[string]Mode, error) {
	names, err := fs.Glob(files, "*.yaml")
	if err != nil {
		return nil, err
	}
	out := map[string]Mode{}
	for _, n := range names {
		b, err := files.ReadFile(n)
		if err != nil {
			return nil, err
		}
		var m Mode
		if err := yaml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if m.ID == "" || m.Voice == "" {
			return nil, fmt.Errorf("%s: id and voice are required", n)
		}
		if err := checkLines(m); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out[m.ID] = m
	}
	return out, nil
}

func checkLines(m Mode) error {
	for phase, ls := range m.Lines {
		for _, l := range ls {
			if n := utf8.RuneCountInString(strings.TrimSpace(l)); n == 0 || n > MaxLineLen {
				return fmt.Errorf("lines.%s: %q must be 1..%d characters", phase, l, MaxLineLen)
			}
		}
	}
	return nil
}

// LoadWithOverrides loads the embedded modes, then applies *.yaml files in
// dir (e.g. %APPDATA%/tunas/modes on Windows) so texts can be changed without
// recompiling. An override replaces only the fields it sets; for lines,
// only the phases it lists. A missing dir is fine. A bad override file is
// skipped and reported in the returned warnings.
func LoadWithOverrides(dir string) (map[string]Mode, []error, error) {
	ms, err := Load()
	if err != nil {
		return nil, nil, err
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	var warns []error
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, err)
			continue
		}
		var o Mode
		if err := yaml.Unmarshal(b, &o); err != nil {
			warns = append(warns, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if err := checkLines(o); err != nil {
			warns = append(warns, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if o.ID == "" {
			o.ID = strings.TrimSuffix(filepath.Base(path), ".yaml")
		}
		m := ms[o.ID]
		m.ID = o.ID
		if o.Name != "" {
			m.Name = o.Name
		}
		if o.Voice != "" {
			m.Voice = o.Voice
		}
		if len(o.Lines) > 0 {
			merged := map[string][]string{}
			for k, v := range m.Lines {
				merged[k] = v
			}
			for k, v := range o.Lines {
				merged[k] = v
			}
			m.Lines = merged
		}
		ms[o.ID] = m
	}
	return ms, warns, nil
}
