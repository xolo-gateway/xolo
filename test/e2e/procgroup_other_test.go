//go:build e2e && unix && !linux

package e2e

import (
	"os/exec"
	"syscall"
)

// isolateServer starts the server in its own process group, which its
// plugins join, so stopServerGroup reaches every process it left behind.
func isolateServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
