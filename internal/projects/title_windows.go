//go:build windows

package projects

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This is the only file in the repository that may call the window title API
// (internal/agent/privacy_test.go enforces it). The title is returned to the
// caller in memory and nowhere else.

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procGetWindowTextW    = user32.NewProc("GetWindowTextW")
	errNoForegroundWindow = errors.New("no foreground window")
)

const titleBufUnits = 1024

type winReader struct{}

// NewReader returns the Windows reader.
func NewReader() TitleReader { return winReader{} }

func (winReader) Foreground() (string, string, error) {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return "", "", errNoForegroundWindow
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 {
		return "", "", errNoForegroundWindow
	}
	exe := processName(pid)
	if exe == "" {
		return "", "", errNoForegroundWindow
	}
	buf := make([]uint16, titleBufUnits)
	n, _, _ := procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return exe, windows.UTF16ToString(buf[:n]), nil
}

func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h) //nolint:errcheck
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:size]))
}

// Probe prints the foreground process and its raw window title once a
// second for d, to w (the console). It exists so the owner can see the real
// titles of an editor and fit a pattern to them; it is run only by an
// explicit `--probe-title`, prints locally and stores nothing.
func Probe(ctx context.Context, w io.Writer, r TitleReader, d time.Duration) {
	fmt.Fprintf(w, "Membaca judul jendela aktif selama %s. Klik editor yang ingin dicek. Tidak ada yang disimpan.\n", d)
	end := time.Now().Add(d)
	last := ""
	for time.Now().Before(end) && ctx.Err() == nil {
		exe, title, err := r.Foreground()
		line := fmt.Sprintf("%s | %s", exe, title)
		if err != nil {
			line = "(tidak ada jendela aktif)"
		}
		if line != last {
			fmt.Fprintln(w, line)
			last = line
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	fmt.Fprintln(w, "Selesai.")
}
