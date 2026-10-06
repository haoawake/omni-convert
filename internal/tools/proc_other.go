//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// trackProcess 把外部程序的优先级调低一点（和 Windows 版的「低于正常」一样），转换时电脑不会卡
func trackProcess(p *os.Process) {
	if p != nil {
		syscall.Setpriority(syscall.PRIO_PROCESS, p.Pid, 5)
	}
}
