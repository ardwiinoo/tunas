package agent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/secret"
)

// CertWarnDays is how long before NotAfter the agent starts to warn (PRD 9c).
const CertWarnDays = 30

// ClientCert is the agent's mTLS client certificate.
type ClientCert struct {
	Cert     tls.Certificate
	NotAfter time.Time
}

// DaysLeft is the whole days until the certificate expires (negative: expired).
func (c *ClientCert) DaysLeft(now time.Time) int {
	return int(c.NotAfter.Sub(now).Hours() / 24)
}

// Warn is the warning text when the certificate expires within CertWarnDays
// ("" when there is nothing to say).
func (c *ClientCert) Warn(now time.Time) string {
	if d := c.DaysLeft(now); d <= CertWarnDays {
		return fmt.Sprintf("sertifikat klien mTLS kedaluwarsa dalam %d hari (%s): buat yang baru dengan scripts/mtls-init.sh", max(d, 0), c.NotAfter.Format("2006-01-02"))
	}
	return ""
}

// LoadClientCert reads the client certificate (PEM) and its private key. The
// key is a PEM file (keyPath) or, when keyPath is empty, the DPAPI-protected
// store written by --set-client-key. A missing, unreadable or expired
// certificate is a clear error, not a vague "cannot reach the server".
func LoadClientCert(certPath, keyPath string, keys secret.Store, now time.Time) (*ClientCert, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("tls.client_cert: cannot read the client certificate (%s): %w", certPath, err)
	}
	var keyPEM []byte
	if keyPath != "" {
		if keyPEM, err = os.ReadFile(keyPath); err != nil {
			return nil, fmt.Errorf("tls.client_key: cannot read the client key (%s): %w", keyPath, err)
		}
	} else if keyPEM, err = keys.Load(); err != nil {
		if errors.Is(err, secret.ErrNotFound) {
			return nil, errors.New("tls.client_key is empty and no key is stored: run `tunas-agent.exe --set-client-key <client.key>`")
		}
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("the client certificate and key do not match or are not PEM")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("the client certificate cannot be parsed")
	}
	pair.Leaf = leaf
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("the client certificate is not valid before %s (check the laptop's clock)", leaf.NotBefore.Format("2006-01-02"))
	}
	if !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("the client certificate expired on %s: make a new one with scripts/mtls-init.sh", leaf.NotAfter.Format("2006-01-02"))
	}
	return &ClientCert{Cert: pair, NotAfter: leaf.NotAfter}, nil
}

// StoreClientKey checks that keyPEM is a PEM private key and stores it
// DPAPI-protected. (Whether it matches the certificate is checked at start.)
func StoreClientKey(keys secret.Store, keyPEM []byte) error {
	blk, _ := pem.Decode(keyPEM)
	if blk == nil || !strings.HasSuffix(blk.Type, "PRIVATE KEY") {
		return errors.New("that file is not a PEM private key")
	}
	return keys.Save(keyPEM)
}
