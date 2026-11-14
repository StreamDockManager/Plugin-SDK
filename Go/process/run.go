package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"os/exec"
	"time"
)

type BoundedOutput struct {
	bytes.Buffer
	overflow bool
	limit    int
}

// Capture output up to the limit while reporting the original write length
func (b *BoundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	limit := b.limit
	if limit <= 0 {
		limit = 1 << 20
	}
	remaining := limit - b.Len()
	if remaining < n {
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p[:max(0, min(n, remaining))])
	return n, nil
}

type Options struct {
	Timeout     time.Duration
	OutputLimit int
	WaitDelay   time.Duration
	Directory   string
	Environment []string
}

// Run a command with default resource limits
func Run(ctx context.Context, name string, args ...string) (string, error) {
	return RunWithOptions(ctx, Options{}, name, args...)
}

// Run a command with explicit timeout and output limits
func RunWithOptions(ctx context.Context, options Options, name string, args ...string) (string, error) {
	if options.Timeout <= 0 {
		options.Timeout = 8 * time.Second
	}
	if options.OutputLimit <= 0 {
		options.OutputLimit = 1 << 20
	}
	if options.WaitDelay <= 0 {
		options.WaitDelay = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	cmd := BackgroundCommandContext(ctx, name, args...)
	cmd.WaitDelay = options.WaitDelay
	cmd.Dir = options.Directory
	cmd.Env = options.Environment
	out, stderr := boundedBuffer{limit: options.OutputLimit}, boundedBuffer{limit: options.OutputLimit}
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if _, ok := errors.AsType[*exec.Error](err); ok {
			return "", fmt.Errorf("%s is not installed or unavailable on PATH", name)
		}
		return "", fmt.Errorf("%s failed; check selection, sign-in and operating-system permissions", name)
	}
	if out.overflow || stderr.overflow {
		return "", fmt.Errorf("%s response exceeds limit", name)
	}
	return out.String(), nil
}

type boundedBuffer = BoundedOutput

// Report whether captured output exceeded its limit
func (b *BoundedOutput) Overflow() bool { return b.overflow }
