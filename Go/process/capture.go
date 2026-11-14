package process

import (
	"fmt"
	"os/exec"
	"time"
)

// CaptureOptions retains caller-owned command configuration and cancellation
type CaptureOptions struct {
	OutputLimit int
	WaitDelay   time.Duration
	Truncate    bool
}

// Capture bounds combined stdout/stderr without creating a shell or starting a timer
func Capture(command *exec.Cmd, options CaptureOptions) (string, error) {
	if options.OutputLimit <= 0 {
		options.OutputLimit = 1 << 20
	}
	if options.WaitDelay <= 0 {
		options.WaitDelay = time.Second
	}
	output := boundedBuffer{limit: options.OutputLimit}
	command.Stdout = &output
	command.Stderr = &output
	command.WaitDelay = options.WaitDelay
	err := command.Run()
	if err == nil && output.overflow && !options.Truncate {
		return output.String(), fmt.Errorf("process response exceeds limit")
	}
	return output.String(), err
}
