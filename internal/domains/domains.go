// Package domains validates and normalizes the website hostnames of Fase 8c.
// Only a plain DNS hostname ever passes: never a path, query, fragment,
// credentials, port, IP address or an internal name like "localhost". The
// extension, the agent and the server all apply these rules, so a page title
// or an address cannot ride along.
package domains

import (
	"regexp"
	"strings"
)

// MaxLen is the longest hostname (RFC 1035).
const MaxLen = 253

var (
	reLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reTLD   = regexp.MustCompile(`^([a-z]{2,63}|xn--[a-z0-9-]{1,59})$`)
)

// Normalize lower-cases a hostname, drops a trailing dot and a leading "www.",
// and reports whether what is left is a plain public-looking DNS name: at
// least two labels, ASCII (an IDN arrives as punycode "xn--"), a letters-only
// top-level label, no IP address, no port.
func Normalize(host string) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	for strings.HasPrefix(h, "www.") { // "www.www.x.com" must come out stable
		h = h[4:]
	}
	if h == "" || len(h) > MaxLen {
		return "", false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", false // "localhost", "intranet", a bare word
	}
	for _, l := range labels {
		if !reLabel.MatchString(l) {
			return "", false // also rejects spaces, slashes, "@", ":", "?", "#", "%", "[", non-ASCII
		}
	}
	if !reTLD.MatchString(labels[len(labels)-1]) {
		return "", false // an IPv4 address ends in digits; names like "a.b1" are not public suffixes
	}
	return h, true
}

// Match returns the entry of m that covers host: the host itself or the
// nearest parent ("m.youtube.com" is covered by "youtube.com"). ok=false
// when nothing matches.
func Match[T any](host string, m map[string]T) (key string, v T, ok bool) {
	for h := host; ; {
		if val, hit := m[h]; hit {
			return h, val, true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 || strings.Count(h, ".") < 2 { // never match a bare TLD
			var zero T
			return "", zero, false
		}
		h = h[i+1:]
	}
}
