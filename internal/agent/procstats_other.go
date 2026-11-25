//go:build !windows

package agent

import "runtime"

type goProc struct{}

// NewProcReader approximates with the Go runtime's numbers outside Windows
// (development only: the agent runs on Windows).
func NewProcReader() ProcReader { return goProc{} }

func (goProc) Read() (ProcSample, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ProcSample{PrivateBytes: ms.Sys, WorkingSet: ms.HeapInuse}, nil
}
