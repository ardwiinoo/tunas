package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// FakeProtector is reversible and visibly different from the plain text.
type FakeProtector struct{ Fail bool }

func (f FakeProtector) Protect(p []byte) ([]byte, error) {
	if f.Fail {
		return nil, errors.New("boom")
	}
	out := []byte("FAKE:")
	for _, b := range p {
		out = append(out, b^0x5a)
	}
	return out, nil
}

func (f FakeProtector) Unprotect(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, []byte("FAKE:")) {
		return nil, errors.New("not ours")
	}
	out := make([]byte, 0, len(b))
	for _, c := range b[5:] {
		out = append(out, c^0x5a)
	}
	return out, nil
}

func TestStoreRoundTripAndNoPlainTextOnDisk(t *testing.T) {
	st := Store{Path: filepath.Join(t.TempDir(), "sub", "token.bin"), P: FakeProtector{}}
	if _, err := st.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	if err := st.Save([]byte("tnd_secret-value")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(st.Path)
	if bytes.Contains(raw, []byte("secret-value")) {
		t.Fatal("the plain token is on disk")
	}
	got, err := st.Load()
	if err != nil || string(got) != "tnd_secret-value" {
		t.Fatalf("%q, %v", got, err)
	}
	// overwriting works and leaves no temp files behind
	if err := st.Save([]byte("tnd_other")); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Load(); string(got) != "tnd_other" {
		t.Fatalf("%q", got)
	}
	ents, _ := os.ReadDir(filepath.Dir(st.Path))
	if len(ents) != 1 {
		t.Fatalf("%d files left in the directory", len(ents))
	}
}

func TestStoreErrors(t *testing.T) {
	dir := t.TempDir()
	if err := (Store{Path: filepath.Join(dir, "a"), P: FakeProtector{Fail: true}}).Save([]byte("x")); err == nil {
		t.Fatal("a failing protector must fail the save")
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
		t.Fatal("a failed save must not leave a file")
	}
	p := filepath.Join(dir, "b")
	_ = os.WriteFile(p, []byte("garbage"), 0o600)
	if _, err := (Store{Path: p, P: FakeProtector{}}).Load(); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("garbage must fail to decrypt, got %v", err)
	}
}
