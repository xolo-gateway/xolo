//go:build e2e

package e2e

import (
	"os/exec"
	"syscall"
)

// stopServerGroup kills whatever remains of the server's process group once
// the server has exited: plugins it failed to stop would otherwise outlive
// the suite.
func stopServerGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
