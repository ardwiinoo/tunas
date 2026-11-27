package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- test PKI -------------------------------------------------------------

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert, key, pool}
}

var (
	ekuClient = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	ekuServer = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
)

func (ca *testCA) issue(t *testing.T, cn string, notAfter time.Time, eku []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: eku,
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func tlsServer(t *testing.T, h http.Handler, cfg *tls.Config) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })

// clientFor builds an agent client that trusts serverCA and presents certs.
func clientFor(url string, serverCA *testCA, certs ...tls.Certificate) *Client {
	c := NewClient(url, "tnd_test-token-0123456789", certs...)
	c.http.Transport.(*http.Transport).TLSClientConfig.RootCAs = serverCA.pool
	return c
}

func get(c *Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out map[string]any
	return c.do(ctx, http.MethodGet, "/x", nil, &out)
}

// ---- mTLS (PRD 9c, agent side) ---------------------------------------------

func TestAgentAgainstMutualTLSServer(t *testing.T) {
	ca, other := newCA(t, "tunas-ca"), newCA(t, "other-ca")
	srv := tlsServer(t, okHandler, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "server", time.Now().Add(time.Hour), ekuServer)},
		ClientAuth:   tls.RequireAndVerifyClientCert, ClientCAs: ca.pool,
	})
	valid := ca.issue(t, "laptop", time.Now().Add(time.Hour), ekuClient)
	expired := ca.issue(t, "laptop", time.Now().Add(-time.Minute), ekuClient)
	foreign := other.issue(t, "laptop", time.Now().Add(time.Hour), ekuClient)
	serverOnly := ca.issue(t, "laptop", time.Now().Add(time.Hour), ekuServer)

	if err := get(clientFor(srv.URL, ca, valid)); err != nil {
		t.Fatalf("a valid client certificate must work: %v", err)
	}
	for name, c := range map[string]*Client{
		"no certificate":            clientFor(srv.URL, ca),
		"certificate of another CA": clientFor(srv.URL, ca, foreign),
		"expired certificate":       clientFor(srv.URL, ca, expired),
		"server-only key usage":     clientFor(srv.URL, ca, serverOnly),
	} {
		if err := get(c); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// ---- TLS verification and version ------------------------------------------

func TestServerCertificateIsVerified(t *testing.T) {
	srv := httptest.NewTLSServer(okHandler) // self-signed: the default client must refuse it
	defer srv.Close()
	err := get(NewClient(srv.URL, "tnd_test-token-0123456789"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("an untrusted certificate must be refused, got %v", err)
	}
}

func TestOldTLSVersionIsRefused(t *testing.T) {
	ca := newCA(t, "ca")
	srv := tlsServer(t, okHandler, &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "s", time.Now().Add(time.Hour), ekuServer)},
		MaxVersion:   tls.VersionTLS11,
	})
	if err := get(clientFor(srv.URL, ca)); err == nil {
		t.Fatal("TLS 1.1 must be refused")
	}
	cfg := NewHTTPClient().Transport.(*http.Transport).TLSClientConfig
	if cfg.MinVersion < tls.VersionTLS12 || cfg.InsecureSkipVerify {
		t.Fatalf("MinVersion %#x, InsecureSkipVerify %v", cfg.MinVersion, cfg.InsecureSkipVerify)
	}
}

// ---- redirects ---------------------------------------------------------------

// The redirect target must not even be asked, so Authorization cannot leak.
func TestRedirectsOutOfTheHostAreRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	otherHost := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)

	// https -> http
	ca := newCA(t, "ca")
	httpsSrv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}), &tls.Config{Certificates: []tls.Certificate{ca.issue(t, "s", time.Now().Add(time.Hour), ekuServer)}})
	if err := get(clientFor(httpsSrv.URL, ca)); !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("https to http: %v", err)
	}

	// another host name, same scheme
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherHost+"/steal", http.StatusTemporaryRedirect)
	}))
	defer hop.Close()
	if err := get(NewClient(hop.URL, "tnd_test-token-0123456789")); !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("other host: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("the redirect target was reached %d times", hits.Load())
	}
}

func TestRedirectOnTheSameHostIsAllowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/y", http.StatusTemporaryRedirect) })
	mux.HandleFunc("/y", okHandler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if err := get(NewClient(srv.URL, "tnd_test-token-0123456789")); err != nil {
		t.Fatal(err)
	}
}
