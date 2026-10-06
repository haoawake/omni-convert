//go:build !windows && !darwin

package office

import (
	"os/exec"

	"github.com/haoawake/omni-convert/internal/conv"
)

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

func errNoOffice() error {
	return conv.Fail("需要安装 LibreOffice 才能转换这类文件", "LibreOffice 是免费的：https://zh-cn.libreoffice.org")
}
