//go:build !windows

// Portable handler teardown: process-group kill so `go run` children die too.
package cli

import (
	"os/exec"
	"syscall"
)

// startDetached puts the handler in its own process group so killTree
// can signal the whole tree (go run -> compiled handler).
func startDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree SIGKILLs the process group; falls back to a single kill when
// the group is already gone (e.g. handler crashed on its own).
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
