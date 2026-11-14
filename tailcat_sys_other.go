//go:build !linux && !windows

package main

import "os/exec"

// setTailcatSysProcAttr is a no-op here; Owldrop's shutdown path stops shares.
func setTailcatSysProcAttr(cmd *exec.Cmd) {}
