package config

import (
	"strings"
	"testing"
)

func TestProjectEditorsConfig(t *testing.T) {
	c, err := ParseAgent([]byte("server_url: https://x.example\ndevice_token: t\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Projects.Editors) != 1 || c.Projects.Editors[0].Process != "Code.exe" {
		t.Fatalf("default editors %+v", c.Projects.Editors)
	}
	// the owner's list replaces the default
	c, err = ParseAgent([]byte(`server_url: https://x.example
device_token: t
projects:
  editors:
    - process: Code.exe
      name: VS Code
      pattern: '^(?P<project>.+?) - Visual Studio Code$'
      no_project: ["Visual Studio Code"]
    - process: Antigravity.exe
      pattern: '^(?P<project>.+?) - Antigravity$'
`))
	if err != nil || len(c.Projects.Editors) != 2 || c.Projects.Editors[1].Process != "Antigravity.exe" {
		t.Fatalf("custom editors %+v %v", c.Projects.Editors, err)
	}
	// a pattern without the project group is refused when the config loads
	_, err = ParseAgent([]byte("server_url: https://x.example\ndevice_token: t\nprojects:\n  editors:\n    - process: Zed.exe\n      pattern: '^(.+) - Zed$'\n"))
	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("bad pattern accepted: %v", err)
	}
}
