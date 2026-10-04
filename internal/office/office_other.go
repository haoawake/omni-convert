//go:build !windows

package office

import "os/exec"

// 非 Windows 系统：没有 Office 自动化，只能用 LibreOffice（如果装了）和纯 Go 的部分。

func detectSystem() (Info, [numApps]progInfo) {
	var info Info
	if p, err := exec.LookPath("soffice"); err == nil {
		info.LibreOffice = p
	}
	return info, [numApps]progInfo{}
}

func newCOMEngine(appKind) engine { return nil }

func shutdownCOM() {}
