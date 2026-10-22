//go:build e2e

package e2e

import "os/exec"

// isolateServer does nothing on Windows, which has no process groups to kill:
// the suite relies on the server stopping its plugins.
func isolateServer(cmd *exec.Cmd) {}

// stopServerGroup cannot see the server's plugins on Windows and reports
// nothing.
func stopServerGroup(cmd *exec.Cmd) (leaked bool) { return false }
