//go:build !linux

package plugin

import "os/exec"

// bindToServer does nothing outside Linux, which has no parent death signal:
// a plugin there only stops when the manager shuts it down.
func bindToServer(cmd *exec.Cmd) {}
