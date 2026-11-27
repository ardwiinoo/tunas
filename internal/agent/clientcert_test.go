package agent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/secret"
)

// writePair writes cert and key PEM files and returns their paths.
func writePair(t *testing.T, dir string, c tls.Certificate) (certPath, keyPath string) {
	t.Helper()
	certPath, keyPath = filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestLoadClientCert(t *testing.T) {
	ca := newCA(t, "ca")
	now := time.Now()
	dir := t.TempDir()
	keys := secret.Store{Path: filepath.Join(dir, "clientkey.bin"), P: fakeP{}}

	good := ca.issue(t, "laptop", now.Add(200*24*time.Hour), ekuClient)
	cp, kp := writePair(t, dir, good)
	cc, err := LoadClientCert(cp, kp, keys, now)
	if err != nil {
		t.Fatal(err)
	}
	if d := cc.DaysLeft(now); d < 199 || d > 200 {
		t.Fatalf("days left %d", d)
	}
	if cc.Warn(now) != "" {
		t.Fatalf("no warning expected with 200 days left: %q", cc.Warn(now))
	}

	// 30 days or less: warning with the days and the way out
	soon := ca.issue(t, "laptop", now.Add(20*24*time.Hour+time.Hour), ekuClient)
	cp2, kp2 := writePair(t, t.TempDir(), soon)
	cc, err = LoadClientCert(cp2, kp2, keys, now)
	if err != nil {
		t.Fatal(err)
	}
	if w := cc.Warn(now); !strings.Contains(w, "20 hari") || !strings.Contains(w, "mtls-init.sh") {
		t.Fatalf("warning %q", w)
	}

	// the key from the DPAPI store (config has no client_key)
	raw, _ := os.ReadFile(kp)
	if err := StoreClientKey(keys, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientCert(cp, "", keys, now); err != nil {
		t.Fatalf("key from the store: %v", err)
	}
	if b, _ := os.ReadFile(keys.Path); strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatal("the key is plain in the store")
	}
}

func TestLoadClientCertErrorsAreClear(t *testing.T) {
	ca := newCA(t, "ca")
	now := time.Now()
	dir := t.TempDir()
	keys := secret.Store{Path: filepath.Join(dir, "clientkey.bin"), P: fakeP{}}
	good := ca.issue(t, "laptop", now.Add(100*24*time.Hour), ekuClient)
	cp, kp := writePair(t, dir, good)
	expired := ca.issue(t, "laptop", now.Add(-time.Hour), ekuClient)
	ecp, ekp := writePair(t, t.TempDir(), expired)
	_, otherKey := writePair(t, t.TempDir(), ca.issue(t, "x", now.Add(time.Hour), ekuClient))

	for name, tc := range map[string]struct {
		cert, key string
		want      string
	}{
		"certificate missing": {filepath.Join(dir, "nope.crt"), kp, "tls.client_cert"},
		"key missing":         {cp, filepath.Join(dir, "nope.key"), "tls.client_key"},
		"nothing stored":      {cp, "", "--set-client-key"},
		"expired":             {ecp, ekp, "expired on"},
		"key of another cert": {cp, otherKey, "do not match"},
	} {
		_, err := LoadClientCert(tc.cert, tc.key, keys, now)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	if err := StoreClientKey(keys, []byte("not a key")); err == nil {
		t.Error("garbage stored as a key")
	}
	if _, err := os.Stat(keys.Path); err == nil {
		t.Error("garbage was written")
	}
}

// The yellow state shows while online with a certificate warning, and goes
// away when the warning does.
func TestCertWarningTurnsTheTrayYellow(t *testing.T) {
	a := &Agent{Log: slog.New(slog.DiscardHandler)}
	var seen []ConnState
	a.OnConn = func(s ConnState) { seen = append(seen, s) }
	a.setConn(ConnOnline)
	a.SetCertWarning("sertifikat segera habis")
	a.SetCertWarning("")
	a.setConn(ConnOffline)
	a.SetCertWarning("lagi")
	a.setConn(ConnOnline)
	want := []ConnState{ConnOnline, ConnOnlineWarn, ConnOnline, ConnOffline, ConnOnlineWarn}
	if len(seen) != len(want) {
		t.Fatalf("states %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("states %v, want %v", seen, want)
		}
	}
}
