//go:build windows

package agent

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SingleInstance takes a named mutex for the current user session. A second
// agent gets ErrAlreadyRunning. The OS releases the mutex when the process
// exits, even after a crash, so a stale lock is impossible.
func SingleInstance(name string) (release func(), err error) {
	n, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, n)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h) //nolint:errcheck
		}
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil //nolint:errcheck
}

// OpenConsole gives a GUI-subsystem build (-H=windowsgui) a console window
// and points stdout/stderr at it. It returns false when the process already
// has a console (e.g. a console build run from a terminal), which is fine.
func OpenConsole(title string) (bool, error) {
	if r, _, err := pAllocConsole.Call(); r == 0 {
		if h, _, _ := pGetConsoleWindow.Call(); h != 0 {
			return false, nil // already has one
		}
		return false, err
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return true, err
	}
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err == nil {
		os.Stdin = in
		windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(in.Fd())) //nolint:errcheck
	}
	os.Stdout, os.Stderr = out, out
	windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd())) //nolint:errcheck
	windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))  //nolint:errcheck
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		pSetConsoleTitle.Call(uintptr(unsafe.Pointer(t))) //nolint:errcheck
	}
	return true, nil
}

var (
	pAllocConsole     = kernel32.NewProc("AllocConsole")
	pGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	pSetConsoleTitle  = kernel32.NewProc("SetConsoleTitleW")
)
