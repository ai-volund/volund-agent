//go:build !linux

package builtin

import "os/exec"

// applyResourceLimits is a no-op on non-Linux platforms.
func applyResourceLimits(_ *exec.Cmd) {}

// wrapWithLimits returns the command unchanged on non-Linux platforms.
func wrapWithLimits(cmd string, args []string) (string, []string) {
	return cmd, args
}
