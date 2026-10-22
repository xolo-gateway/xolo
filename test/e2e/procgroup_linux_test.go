//go:build e2e

package e2e

import (
	"os/exec"
	"syscall"
)

// isolateServer starts the server in its own process group, which its
// plugins join, so stopServerGroup reaches every process it left behind. The
// server receives SIGTERM when the test binary dies, interrupted or killed
// before its deferred calls run, and stops its plugins on the way out.
func isolateServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
