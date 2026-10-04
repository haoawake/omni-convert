//go:build !windows

package main

import (
	"fmt"
	"os"
)

var shotFile, shotSteps string

// 界面只有 Windows 版；其他系统上可以用命令行模式（-to …）

func runApp([]string) {
	fmt.Fprintln(os.Stderr, appName+"的界面只支持 Windows。可以用命令行模式：-to img:jpg 文件…")
	os.Exit(1)
}

func forwardToRunning([]string) bool { return false }
func attachConsole()                 {}
