//go:build windows

package process

import "syscall"

// Read the platform console-window handle for process tests
func testConsoleWindow() uintptr {
	window, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	return window
}
