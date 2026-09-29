//go:build windows

// Portable handler teardown: tree kill so `go run` children die too.
package cli

import (
	"io"
	"os/exec"
	"strconv"
)

// startDetached is a no-op on Windows; killTree uses taskkill /T instead.
func startDetached(_ *exec.Cmd) {}

// killTree kills the process tree (taskkill /T reaches the handler exe
// spawned by `go run`); falls back to a single kill when taskkill fails.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.Stdout = io.Discard
	kill.Stderr = io.Discard
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
