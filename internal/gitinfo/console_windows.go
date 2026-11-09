package gitinfo

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideConsole stops a console window flashing on every tool call when the
// server was started by a GUI client.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
