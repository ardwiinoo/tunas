// Package secret keeps the agent's secrets (device token, client key) on disk
// encrypted so that only the Windows account that stored them can read them.
//
// There is no crypto of our own here: encryption is Windows DPAPI
// (CryptProtectData), reached through the Protector interface so the logic
// can be tested on any OS with a fake.
package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound means nothing was stored yet.
var ErrNotFound = errors.New("secret not stored")

// Protector encrypts and decrypts a blob for the current user.
type Protector interface {
	Protect(plain []byte) ([]byte, error)
	Unprotect(blob []byte) ([]byte, error)
}

// Store is one encrypted file.
type Store struct {
	Path string
	P    Protector
}

// Save encrypts plain and writes it atomically (temp file, then rename), so a
// crash never leaves a half-written file. The file is private to the owner.
func (s Store) Save(plain []byte) error {
	blob, err := s.P.Protect(plain)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	return WriteFileAtomic(s.Path, blob, 0o600)
}

// Load reads and decrypts. A missing file is ErrNotFound.
func (s Store) Load() ([]byte, error) {
	blob, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.P.Unprotect(blob)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w (stored by another Windows account? store it again with --set-token)", filepath.Base(s.Path), err)
	}
	return plain, nil
}

// WriteFileAtomic writes data next to path and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
