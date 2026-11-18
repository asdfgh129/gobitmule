//go:build !windows

package postproc

import (
	"os/exec"
	"syscall"
)

// setProcessGroup gives ffmpeg its own process group so cancellation cannot
// leave encoder children writing to the .part file (ADR-0012 decision 6).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree kills the whole ffmpeg group, not just the direct child.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The process may already have exited; a direct kill is the last resort.
		return cmd.Process.Kill()
	}
	return nil
}
