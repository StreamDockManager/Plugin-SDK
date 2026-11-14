//go:build !windows

package process

// Read the platform console-window handle for process tests
func testConsoleWindow() uintptr { return 0 }
