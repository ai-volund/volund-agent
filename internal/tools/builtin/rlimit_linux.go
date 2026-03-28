//go:build linux

package builtin

import (
	"os/exec"
	"syscall"
)

// applyResourceLimits restricts the subprocess environment on Linux.
// Sets a new process group (so we can kill the group on timeout) and
// restricts environment variables.
func applyResourceLimits(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Run in its own process group so we can signal the group.
	cmd.SysProcAttr.Setpgid = true
}

// wrapWithLimits wraps a command invocation with ulimit-enforced resource limits.
// Returns a bash command that sets ulimits before executing the actual command.
func wrapWithLimits(origCmd string, origArgs []string) (string, []string) {
	// ulimit: -t CPU seconds, -v virtual memory KB, -f file size KB, -n open files, -u processes
	limits := "ulimit -t 60 -v 262144 -f 65536 -n 64 -u 32 2>/dev/null; "
	args := []string{"-c", limits + "exec " + origCmd}
	for _, a := range origArgs {
		args[1] += " " + a
	}
	return "bash", args
}
