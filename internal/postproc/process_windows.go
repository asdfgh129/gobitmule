//go:build windows

package postproc

import (
	"os/exec"
	"strconv"
)

// Windows has no process groups in the Unix sense. cmd.SysProcAttr is left
// alone; the taskkill tree termination below covers ffmpeg's children.
func setProcessGroup(*exec.Cmd) {}

// killProcessTree mirrors ADR-0012 decision 6 on Windows: /T kills descendants,
// /F forces termination so no encoder child keeps writing the .part file.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
