//go:build windows

package secret

import (
	"bytes"
	"path/filepath"
	"testing"
)

// Real DPAPI (Windows only; CI on Linux does not run this).
func TestDPAPIRoundTrip(t *testing.T) {
	st := Store{Path: filepath.Join(t.TempDir(), "token.bin"), P: DPAPI{}}
	secret := []byte("tnd_real-dpapi-roundtrip-0123456789")
	if err := st.Save(secret); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("%q, %v", got, err)
	}
	if _, err := (DPAPI{}).Unprotect([]byte("not a dpapi blob")); err == nil {
		t.Fatal("garbage must not decrypt")
	}
	if _, err := (DPAPI{}).Unprotect(nil); err == nil {
		t.Fatal("empty must not decrypt")
	}
}
