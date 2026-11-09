//go:build !windows

package gitinfo

import "os/exec"

func hideConsole(*exec.Cmd) {}
