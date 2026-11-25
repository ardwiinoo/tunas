//go:build windows

package agent

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCountersEx is PROCESS_MEMORY_COUNTERS_EX.
type processMemoryCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")

type winProc struct{}

// NewProcReader reads this process through GetProcessMemoryInfo (private
// bytes, the same number as Task Manager's Memory column) and GetProcessTimes.
func NewProcReader() ProcReader { return winProc{} }

func (winProc) Read() (ProcSample, error) {
	h := windows.CurrentProcess()
	var mc processMemoryCountersEx
	mc.CB = uint32(unsafe.Sizeof(mc))
	if r, _, err := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.CB)); r == 0 {
		return ProcSample{}, errors.New("GetProcessMemoryInfo: " + err.Error())
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return ProcSample{}, err
	}
	ft := func(f windows.Filetime) time.Duration { // 100 ns units
		return time.Duration(uint64(f.HighDateTime)<<32|uint64(f.LowDateTime)) * 100
	}
	return ProcSample{PrivateBytes: uint64(mc.PrivateUsage), WorkingSet: uint64(mc.WorkingSetSize), CPUTime: ft(kernel) + ft(user)}, nil
}
