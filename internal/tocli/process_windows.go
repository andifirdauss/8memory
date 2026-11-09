//go:build windows

package tocli

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// prepareProcess keeps the CLI and its tools off the console the TUI is
// drawing on, and makes cancelling stop the tools it started too.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
		if kill.Run() != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
