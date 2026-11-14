package process

import (
	"context"
	"os/exec"
)

// Create a command configured for background execution
func BackgroundCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureBackground(cmd)
	return cmd
}

// Create a cancellable command configured for background execution
func BackgroundCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configureBackground(cmd)
	return cmd
}
