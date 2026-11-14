package benchmarkclock

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	qpc      = kernel32.NewProc("QueryPerformanceCounter")
	qpf      = kernel32.NewProc("QueryPerformanceFrequency")
)

// Create a Windows performance-counter clock
func platformClock() (*Clock, error) {
	// Go's Windows interrupt-time clock can repeat across short loopback runs
	// QPC measures sub-millisecond intervals without a global timer change
	// https://learn.microsoft.com/windows/win32/sysinfo/acquiring-high-resolution-time-stamps
	for _, proc := range []*syscall.LazyProc{qpc, qpf} {
		if err := proc.Find(); err != nil {
			return nil, err
		}
	}
	frequency, err := queryCounter(qpf)
	if err != nil {
		return nil, err
	}
	// The frequency is fixed at boot; retain it for every sample in this run
	return &Clock{frequency: frequency, read: func() (int64, error) {
		return queryCounter(qpc)
	}}, nil
}

// Read a Windows performance-counter value and report API failure
func queryCounter(proc *syscall.LazyProc) (int64, error) {
	var value int64 // Win32 LARGE_INTEGER; the API writes it synchronously
	ok, _, err := proc.Call(uintptr(unsafe.Pointer(&value)))
	if ok == 0 {
		if err != syscall.Errno(0) {
			return 0, fmt.Errorf("%s failed: %w", proc.Name, err)
		}
		return 0, fmt.Errorf("%s failed", proc.Name)
	}
	return value, nil
}
