//go:build windows

package process

import (
	"os/exec"
	"syscall"
)

// Suppress a console window for background Windows commands
func configureBackground(cmd *exec.Cmd) {

	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
