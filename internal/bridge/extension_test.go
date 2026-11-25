package bridge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/config"
)

// The extension may ask for the least it can work with (PRD 8c): tabs and
// storage, nothing broad, nothing for private windows, nothing that reads
// pages. This guards the manifest in the repository.
func TestExtensionManifestAsksForLittle(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "extension", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["manifest_version"] != float64(3) {
		t.Errorf("manifest_version %v, want 3", m["manifest_version"])
	}
	perms, _ := m["permissions"].([]any)
	var got []string
	for _, p := range perms {
		got = append(got, p.(string))
	}
	slices.Sort(got)
	if strings.Join(got, ",") != "storage,tabs" {
		t.Errorf("permissions %v, want exactly tabs and storage", got)
	}
	for _, banned := range []string{"host_permissions", "optional_permissions", "optional_host_permissions", "content_scripts",
		"externally_connectable", "web_accessible_resources", "declarative_net_request", "devtools_page", "commands"} {
		if _, ok := m[banned]; ok {
			t.Errorf("manifest has %q", banned)
		}
	}
	if m["incognito"] != "not_allowed" {
		t.Errorf("incognito = %v: the extension must not run in private windows", m["incognito"])
	}
	// the manifest key fixes the extension id; it must be the id the agent expects by default
	key, _ := m["key"].(string)
	der, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(der) < 100 {
		t.Fatalf("manifest key: %v", err)
	}
	sum := sha256.Sum256(der)
	id := make([]byte, 32)
	for i := 0; i < 16; i++ {
		id[2*i], id[2*i+1] = 'a'+sum[i]>>4, 'a'+sum[i]&0xf
	}
	if string(id) != config.DefaultExtensionID {
		t.Errorf("the manifest key gives id %s, the agent default is %s", id, config.DefaultExtensionID)
	}
}

// No file of the extension reads page content or sends more than a host.
func TestExtensionSourceStaysSmall(t *testing.T) {
	dir := filepath.Join("..", "..", "extension")
	for _, f := range []string{"background.js", "host.js", "options.js"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, banned := range []string{"executeScript", "content_scripts", "document.title", ".title", "chrome.history", "chrome.cookies",
			"chrome.webRequest", "scripting", "innerText", "tab.url,", "url:"} {
			if strings.Contains(src, banned) && !(f == "background.js" && banned == ".title") {
				t.Errorf("%s uses %q", f, banned)
			}
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "background.js"))
	if strings.Contains(string(b), "tab.title") || strings.Contains(string(b), "pendingUrl") {
		t.Error("the background script reads a tab title")
	}
	// the request body is exactly {host, ts}
	if !strings.Contains(string(b), "JSON.stringify({ host, ts: Math.floor(now / 1000) })") {
		t.Error("the request body changed: it must stay {host, ts}")
	}
}
