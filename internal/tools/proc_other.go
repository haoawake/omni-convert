//go:build !windows

package tools

import (
	"os"
	"os/exec"
)

func hideWindow(*exec.Cmd)     {}
func trackProcess(*os.Process) {}
