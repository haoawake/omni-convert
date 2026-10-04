//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole 正式版是窗口程序，没有控制台。从命令行运行时借用父进程的控制台来输出。
func attachConsole() {
	if _, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil {
		if h, _ := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); h != 0 && h != windows.InvalidHandle {
			return // 已经有输出（比如被重定向到文件或管道）
		}
	}
	const attachParent = ^uint32(0) // ATTACH_PARENT_PROCESS
	if r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole").Call(uintptr(attachParent)); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
	}
	windows.SetConsoleOutputCP(65001)
}
