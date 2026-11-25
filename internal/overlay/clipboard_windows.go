//go:build windows

package overlay

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The clipboard is read only on the owner's explicit action (Ctrl+V in the
// bubble, or the tray item): the text goes to the bubble or the preview and
// nowhere else.

var (
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pGetClipboardData = user32.NewProc("GetClipboardData")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalSize       = kernel32.NewProc("GlobalSize")
)

const cfUnicodeText = 13

// maxClipboard bounds what is read (UTF-16 units); far above a 50-entry paste.
const maxClipboard = 1 << 20

// readClipboard returns the clipboard's Unicode text.
func readClipboard() (string, error) {
	// another program may hold the clipboard for a moment
	opened := false
	for i := 0; i < 10; i++ {
		if r, _, _ := pOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		return "", errors.New("clipboard busy")
	}
	defer pCloseClipboard.Call() //nolint:errcheck

	h, _, _ := pGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", errors.New("no text on the clipboard")
	}
	size, _, _ := pGlobalSize.Call(h)
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("clipboard not readable")
	}
	defer pGlobalUnlock.Call(h) //nolint:errcheck

	n := int(size) / 2
	if n > maxClipboard {
		n = maxClipboard
	}
	// p is memory owned by Windows (not the Go heap) and stays valid until
	// GlobalUnlock; reading its address back as a pointer is safe here.
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	units := unsafe.Slice((*uint16)(ptr), n)
	end := 0
	for end < len(units) && units[end] != 0 {
		end++
	}
	return windows.UTF16ToString(units[:end]), nil
}
