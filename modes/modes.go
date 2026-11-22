// Package modes embeds the per-form personas (modes/*.yaml).
package modes

import (
	"embed"
	"fmt"
	"io/fs"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var files embed.FS

// Mode is a form persona.
type Mode struct {
	ID     string `yaml:"id"`
	Name   string `yaml:"name"`
	Voice  string `yaml:"voice"`
	Sprite string `yaml:"sprite"`
}

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
		out[m.ID] = m
	}
	return out, nil
}
