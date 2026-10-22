package plugin

import (
	"os/exec"
	"syscall"
)

// bindToServer has the kernel kill the plugin when the server dies, even from
// a SIGKILL that leaves Shutdown no chance to run.
//
// The signal fires when the OS thread that started the plugin exits, not the
// process. The Go runtime only retires a thread when a goroutine that locked
// itself to it with runtime.LockOSThread returns without unlocking. Neither
// the server nor its dependencies do that today, so the plugins die with the
// server and not before. Code that does would kill every plugin started from
// the retired thread while the server still runs.
func bindToServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
