package plugin

import (
	"os/exec"
	"syscall"
)

// bindToServer has the kernel kill the plugin when the server dies, even from
// a SIGKILL that leaves Shutdown no chance to run. The signal follows the OS
// thread that started the plugin, not the whole process: the Go runtime only
// retires a thread when a goroutine locked to it returns, which the manager
// never does.
func bindToServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
