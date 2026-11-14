//go:build !windows

package process

import (
	"os/exec"
)

// Retain the platform default process window behavior
func configureBackground(*exec.Cmd) {}
