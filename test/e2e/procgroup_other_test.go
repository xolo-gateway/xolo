//go:build e2e && !linux

package e2e

import "os/exec"

// isolateServer leaves the server in the test binary's process group outside
// Linux. Without a parent death signal to stop it, the server must keep
// receiving the Ctrl-C that interrupts the suite.
func isolateServer(cmd *exec.Cmd) {}

// stopServerGroup cannot see the server's plugins outside Linux and reports
// nothing: the server log still tells whether it stopped them.
func stopServerGroup(cmd *exec.Cmd) (leaked bool) { return false }
