//go:build e2e && unix

package e2e

import (
	"os/exec"
	"syscall"
	"time"
)

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
