package agent

import (
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"time"
)

// maxRedirects bounds a redirect chain (the API does not redirect at all;
// a chain longer than this is a misconfiguration or an attack).
const maxRedirects = 3

// ErrUnsafeRedirect is returned when the server tries to send the agent to
// another host or from https to http. The Authorization header must never
// follow such a redirect, so the request is not made at all.
var ErrUnsafeRedirect = errors.New("redirect to another host or from https to http refused")

// NewHTTPClient builds the agent's HTTP client (PRD 9a): TLS 1.2 at least
// (1.3 is used when the server offers it), certificate and host name always
// verified (there is no way to switch that off), optional client certificates
// for mTLS (9c), and no redirects that leave the host or downgrade to http.
func NewHTTPClient(certs ...tls.Certificate) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: certs}
	return &http.Client{Timeout: 40 * time.Second, Transport: tr, CheckRedirect: refuseUnsafeRedirect}
}

func refuseUnsafeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errors.New("too many redirects")
	}
	prev := via[len(via)-1].URL
	if prev.Scheme == "https" && req.URL.Scheme != "https" {
		return ErrUnsafeRedirect
	}
	if !strings.EqualFold(prev.Host, req.URL.Host) || !strings.EqualFold(via[0].URL.Host, req.URL.Host) {
		return ErrUnsafeRedirect
	}
	return nil
}
