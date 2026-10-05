//go:build windows

package tools

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.BELOW_NORMAL_PRIORITY_CLASS,
	}
}

// 所有启动的外部程序都放进同一个「作业对象」：本程序退出（哪怕是崩溃）时系统会把它们一起结束，
// 不会留下还在后台转码的 ffmpeg。
var (
	jobOnce sync.Once
	job     windows.Handle
)

func trackProcess(p *os.Process) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return
		}
		job = h
	})
	if job == 0 || p == nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return
	}
	windows.AssignProcessToJobObject(job, h)
	windows.CloseHandle(h)
}
