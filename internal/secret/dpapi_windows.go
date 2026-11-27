//go:build windows

package secret

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiEntropy is mixed into the key so another program of the same user does
// not decrypt the file by calling CryptUnprotectData with no entropy. It is
// not a secret; the protection is the Windows account.
var dpapiEntropy = []byte("tunas-agent/secret/v1")

// DPAPI is the Windows Data Protection API, bound to the current user.
type DPAPI struct{}

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

// Protect implements Protector.
func (DPAPI) Protect(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptProtectData(blob(plain), nil, blob(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:errcheck
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// Unprotect implements Protector.
func (DPAPI) Unprotect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty blob")
	}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(blob(b), nil, blob(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:errcheck
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// Default is the protector the agent uses.
func Default() Protector { return DPAPI{} }
