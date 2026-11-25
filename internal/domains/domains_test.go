package domains

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	good := map[string]string{
		"youtube.com":             "youtube.com",
		"YouTube.COM":             "youtube.com",
		"www.youtube.com":         "youtube.com",
		"WWW.Example.Co.ID.":      "example.co.id",
		"m.youtube.com":           "m.youtube.com",
		"  github.com  ":          "github.com",
		"a-b.example.org":         "a-b.example.org",
		"xn--bcher-kva.example":   "xn--bcher-kva.example", // punycode (what a browser's hostname gives for an IDN)
		"1password.com":           "1password.com",
		"sub.domain.example.tech": "sub.domain.example.tech",
	}
	for in, want := range good {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	bad := []string{
		"", " ", ".", "localhost", "intranet", "printer.local1", "com",
		"https://youtube.com", "youtube.com/watch?v=abc", "youtube.com/", "youtube.com?x=1", "youtube.com#frag",
		"user:pass@youtube.com", "user@youtube.com", "youtube.com:8080", "youtube.com:", "[::1]", "::1", "[2001:db8::1]",
		"192.168.1.10", "10.0.0.1", "127.0.0.1", "8.8.8.8", "1.2.3.4.5", "0x7f.0.0.1",
		"bücher.example", "例え.jp", "yoütube.com", "you tube.com", "you\ttube.com", "you\ntube.com", "you\x00tube.com",
		"-bad.example.com", "bad-.example.com", "a..b.com", ".example.com", "exa_mple.com", "exa%2Fmple.com",
		"a.b1", "a.123", "example.c", "example.com..", "www.", "www", strings.Repeat("a", 64) + ".com",
		strings.Repeat("a.", 130) + "com", "evil.com\\path", "evil.com;rm", "evil.com,other.com",
	}
	for _, in := range bad {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) accepted as %q", in, got)
		}
	}
	// the longest allowed name passes, one more character does not
	long := strings.Repeat(strings.Repeat("a", 61)+".", 4) + "com" // 4*62 + 3 = 251
	if _, ok := Normalize(long); !ok {
		t.Errorf("a 251 character name was refused")
	}
	at253 := strings.Repeat(strings.Repeat("a", 61)+".", 4) + "ab" + strings.Repeat("", 0) // 4*62 + 2 = 250
	at253 += "c"                                                                           // 251
	if n := 253 - len(at253); n > 0 {
		at253 = strings.Repeat("b", n-1) + "." + at253 // exactly 253 characters
	}
	if len(at253) != 253 {
		t.Fatalf("test name is %d characters", len(at253))
	}
	if _, ok := Normalize(at253); !ok {
		t.Errorf("a 253 character name was refused")
	}
	if _, ok := Normalize("b" + at253); ok {
		t.Errorf("a 254 character name was accepted")
	}
	if got, ok := Normalize("www.www.example.com"); !ok || got != "example.com" {
		t.Errorf("www.www: %q %v", got, ok)
	}
}

func TestMatchSuffix(t *testing.T) {
	m := map[string]string{"youtube.com": "distract", "github.com": "work", "co.id": "bogus"}
	for host, want := range map[string]string{
		"youtube.com": "distract", "m.youtube.com": "distract", "a.b.youtube.com": "distract", "gist.github.com": "work",
	} {
		if _, got, ok := Match(host, m); !ok || got != want {
			t.Errorf("Match(%q) = %q, %v", host, got, ok)
		}
	}
	for _, host := range []string{"notyoutube.com", "youtube.com.evil.com", "example.com", "evil-github.com", "x.co.id"} {
		if k, got, ok := Match(host, m); ok && host != "x.co.id" {
			t.Errorf("Match(%q) matched %q (%s)", host, k, got)
		}
	}
	// a parent that is only a public suffix is a valid entry the owner chose; the
	// last label alone ("com") is never tried
	if _, _, ok := Match("example.com", map[string]string{"com": "x"}); ok {
		t.Error("a bare TLD must not match")
	}
}

// FuzzNormalize: whatever goes in, an accepted result is a plain hostname:
// no path, query, fragment, credentials, port, space or upper case, it is
// stable when normalized again, and it never exceeds the limit.
func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"youtube.com", "https://user:pass@evil.com:8080/path?q=1#frag", "WWW.Example.COM.", "[::1]", "192.168.0.1",
		"xn--bcher-kva.example", "bücher.example", "a..b", "evil.com/x", "a@b.com", "\x00.com", "a.b.c.d.e.f.g.h.i.j.k.l"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, ok := Normalize(in)
		if !ok {
			return
		}
		if len(out) > MaxLen || out != strings.ToLower(out) || strings.ContainsAny(out, "/?#@:\\ \t\r\n[]%;,'\"<>*") || strings.HasPrefix(out, "www.") {
			t.Fatalf("Normalize(%q) = %q is not a plain hostname", in, out)
		}
		if again, ok := Normalize(out); !ok || again != out {
			t.Fatalf("Normalize is not stable: %q → %q → %q", in, out, again)
		}
		for _, r := range out {
			if r > 0x7e || r < 0x20 {
				t.Fatalf("Normalize(%q) = %q has a non-ASCII or control character", in, out)
			}
		}
	})
}
