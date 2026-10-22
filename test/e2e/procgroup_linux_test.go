//go:build e2e

package e2e

import (
	"os/exec"
	"syscall"
	"time"
)

// isolateServer starts the server in its own process group, which its
// plugins join, so stopServerGroup reaches every process it left behind.
// Leaving the test binary's group, the server no longer receives a Ctrl-C:
// it receives SIGTERM instead when the test binary dies, interrupted or
// killed before its deferred calls run, and stops its plugins on the way out.
func isolateServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// stopServerGroup waits for the server's process group to empty once the
// server has exited, then kills whatever remains so nothing outlives the
// suite. It reports whether anything remained: a plugin the server failed to
// stop. A plugin killed with the server stays in the group until its new
// parent reaps it, hence the wait.
func stopServerGroup(cmd *exec.Cmd) (leaked bool) {
	pgid := cmd.Process.Pid
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return false
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return true
}
