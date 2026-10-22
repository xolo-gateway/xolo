package plugin

import (
	"os/exec"
	"syscall"
)

// bindToServer has the kernel kill the plugin when the server dies, even from
// a SIGKILL that leaves Shutdown no chance to run.
//
// The signal fires when the OS thread that started the plugin exits, not the
// process. The Go runtime only retires a thread when a goroutine locked to it
// with runtime.LockOSThread returns, so today the plugins die with the server
// and not before. loadPlugin must therefore never run on a goroutine holding
// runtime.LockOSThread, nor on a thread created by cgo: every plugin started
// there would be killed as soon as that goroutine returns, while the server
// still runs.
func bindToServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
