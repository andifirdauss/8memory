//go:build !windows

package tocli

import (
	"os/exec"
	"syscall"
)

// prepareProcess gives the CLI its own process group, so stopping it also
// reaches the tools it started. SIGINT lets it end the turn properly.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
}
