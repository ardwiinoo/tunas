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
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pEmptyClipboard   = user32.NewProc("EmptyClipboard")
	pSetClipboardData = user32.NewProc("SetClipboardData")
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

// writeClipboard puts text on the clipboard (only for "Salin token ekstensi
// browser", on the owner's click).
func writeClipboard(text string) error {
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	const gmemMoveable = 0x2
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if h == 0 {
		return errors.New("out of memory")
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h) //nolint:errcheck
		return errors.New("clipboard memory not lockable")
	}
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	copy(unsafe.Slice((*uint16)(ptr), len(u)), u)
	pGlobalUnlock.Call(h) //nolint:errcheck

	opened := false
	for i := 0; i < 10; i++ {
		if r, _, _ := pOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		pGlobalFree.Call(h) //nolint:errcheck
		return errors.New("clipboard busy")
	}
	defer pCloseClipboard.Call() //nolint:errcheck
	pEmptyClipboard.Call()       //nolint:errcheck
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h) //nolint:errcheck
		return errors.New("clipboard not writable")
	}
	return nil // the clipboard owns the memory now
}
