package main

import (
	"os/exec"
	"syscall"
)

// setTailcatSysProcAttr keeps the console-subsystem tailcat.exe from flashing
// a console window when launched from the GUI app.
func setTailcatSysProcAttr(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
