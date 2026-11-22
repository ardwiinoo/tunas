//go:build windows

package agent

import (
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// WindowsSampler reads the foreground process name and idle time via Win32.
type WindowsSampler struct{}

// NewSampler returns the platform sampler.
func NewSampler() Sampler { return WindowsSampler{} }

// Sample implements Sampler. Only the executable name is read; the window
// title is never queried.
func (WindowsSampler) Sample() (Sample, error) {
	idle, err := idleTime()
	if err != nil {
		return Sample{}, err
	}
	return Sample{App: foregroundExe(), Idle: idle}, nil
}

func foregroundExe() string {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 {
		return ""
	}
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

func idleTime() (time.Duration, error) {
	info := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if r, _, err := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, err
	}
	tick, _, _ := procGetTickCount.Call()
	// Both are 32-bit millisecond tick counts; unsigned subtraction handles wraparound.
	return time.Duration(uint32(tick)-info.dwTime) * time.Millisecond, nil
}
