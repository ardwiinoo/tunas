package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ValidateServerURL checks the agent's server_url (PRD 9a). It must be
// https://. Plain http:// is allowed only for a loopback host, decided on the
// parsed hostname (never by looking for a substring, prefix or suffix of the
// raw string): exactly "localhost" (any case) or an IP that is loopback.
//
// The errors never repeat the URL: it may carry user:password.
func ValidateServerURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("server_url is not a valid URL")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("server_url has no host")
	}
	if u.User != nil {
		return errors.New("server_url must not contain user info (user@host)")
	}
	switch u.Scheme { // url.Parse already lower-cases the scheme
	case "https":
		return nil
	case "http":
		if isLoopbackHost(host) {
			return nil
		}
		return errors.New("server_url must be https:// (http:// is only allowed for localhost, 127.0.0.1 and [::1])")
	}
	return errors.New("server_url must be https://")
}

// isLoopbackHost reports whether the hostname is exactly "localhost" (ignoring
// case) or a loopback IP literal. "localhost." (trailing dot), "foo.localhost",
// "0.0.0.0" and a bare decimal such as 2130706433 are not.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
