package config

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestValidateServerURL(t *testing.T) {
	ok := []string{
		"https://tunas.ardwiinoo.my.id",
		"https://tunas.ardwiinoo.my.id/",
		"HTTPS://Tunas.Example.com:8443/base",
		"https://localhost",
		"https://127.0.0.1:8080",
		"http://localhost",
		"http://LOCALHOST:8080",
		"http://LocalHost",
		"http://127.0.0.1",
		"http://127.0.0.1:8080",
		"http://127.1.2.3:8080", // all of 127.0.0.0/8 is loopback
		"http://[::1]",
		"http://[::1]:8080",
		"HTTP://127.0.0.1:8080", // scheme case
	}
	for _, s := range ok {
		if err := ValidateServerURL(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	bad := []string{
		"http://localhost.com",
		"http://127.0.0.1.evil.com",
		"http://localhost@evil.com",
		"http://evil.com/localhost",
		"http://foo.localhost",
		"http://localhost.", // trailing dot
		"http://LOCALHOST.",
		"http://0.0.0.0",
		"http://2130706433", // decimal form of 127.0.0.1
		"http://0x7f000001",
		"http://127.0.0.1@evil.com",
		"http://evil.com#localhost",
		"http://evil.com?h=127.0.0.1",
		"http://[::]",
		"http://[::2]",
		"http://[fe80::1]",
		"http://tunas.ardwiinoo.my.id",
		"http://evil.com",
		"HTTP://EVIL.COM",
		"ftp://localhost",
		"file:///etc/passwd",
		"ws://localhost",
		"javascript:alert(1)",
		"localhost:8080",
		"//localhost",
		"tunas.ardwiinoo.my.id",
		"https://user:pass@tunas.example.com", // credentials in the URL
		"https://",
		"http://",
		"",
		"   ",
		"https://exa mple.com",
	}
	for _, s := range bad {
		err := ValidateServerURL(s)
		if err == nil {
			t.Errorf("%q must be rejected", s)
			continue
		}
		if strings.Contains(err.Error(), "pass") && s != "" {
			t.Errorf("%q: the error repeats the URL: %v", s, err)
		}
	}
}

// The decision is made on the parsed host, so an accepted http URL always has
// a loopback hostname.
func FuzzValidateServerURL(f *testing.F) {
	for _, s := range []string{"http://localhost", "http://127.0.0.1.evil.com", "https://x.y", "http://[::1]:1", "http://a@b", "", "%%", "http://localhost%2f@evil.com"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidateServerURL(s) != nil {
			return
		}
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil {
			t.Fatalf("accepted %q but it does not parse", s)
		}
		if u.User != nil || u.Hostname() == "" {
			t.Fatalf("accepted %q with user info or no host", s)
		}
		switch u.Scheme {
		case "https":
		case "http":
			h := u.Hostname()
			if !strings.EqualFold(h, "localhost") {
				if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
					t.Fatalf("accepted http for non-loopback host %q (%q)", h, s)
				}
			}
		default:
			t.Fatalf("accepted scheme %q (%q)", u.Scheme, s)
		}
	})
}

// The agent refuses to start on an unsafe server_url, and says why without
// repeating the URL.
func TestAgentConfigRejectsUnsafeServerURL(t *testing.T) {
	_, err := ParseAgent([]byte("server_url: \"http://tunas.ardwiinoo.my.id\"\n"))
	if err == nil || !strings.Contains(err.Error(), "https://") || strings.Contains(err.Error(), "ardwiinoo") {
		t.Fatalf("got %v", err)
	}
	if _, err := ParseAgent([]byte("server_url: \"http://127.0.0.1:8080\"\n")); err != nil {
		t.Fatal(err)
	}
}
