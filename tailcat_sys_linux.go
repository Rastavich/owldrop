package main

import (
	"os/exec"
	"syscall"
)

// setTailcatSysProcAttr makes the kernel kill tailcat if Owldrop dies without
// running its shutdown path (crash, SIGKILL), so a share's address never
// outlives the app.
func setTailcatSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
