//go:build !windows

package main

// macOS（和其他类 Unix 系统）上的设置位置、保存位置和说明文字。

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// 文档页上没有 LibreOffice 时的提醒、PDF 转 Word 的说明、「关于」里的说明
const (
	noOfficeBanner = "没有找到 LibreOffice，Word、Excel、PPT 相关的转换做不了（CSV 转 Excel、Markdown 转网页不受影响）。LibreOffice 是免费的，装好后回到这里就能用。Mac 上的 Microsoft Office 不能在后台自动转换，所以用的是 LibreOffice。"
	noWordHint     = "没有找到 LibreOffice，只能提取文字做成 Word 文档（版面不保留）"
	pdfToWordHint  = "转成可以编辑的 Word 文档（用 LibreOffice 识别版面，复杂的排版可能会乱）"
	officeAbout    = "Word、Excel、PPT 的转换调用电脑上的 LibreOffice（免费，需要自己安装）。"
)

// settingsDir 是保存设置的文件夹：~/Library/Application Support/万能格式转换
func settingsDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, appName)
}

var (
	deniedMu  sync.Mutex
	deniedDir string // 最近一次 macOS 不让访问的文件夹（「隐私与安全性」里没有允许）
)

// takeDenied 取出并清掉最近一次被系统拒绝访问的文件夹
func takeDenied() string {
	deniedMu.Lock()
	defer deniedMu.Unlock()
	d := deniedDir
	deniedDir = ""
	return d
}

// writable 判断能不能在这个文件夹里写文件。第一次写「下载」「文稿」「桌面」时 macOS 会问用户允不允许，
// 这一步会等用户回答；用户没有允许时记下这个文件夹（界面上会解释怎么打开权限）。
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".omni-*.tmp")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			deniedMu.Lock()
			deniedDir = dir
			deniedMu.Unlock()
		}
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// defaultOutDir 是原文件夹不能写时的去处：「文稿/万能格式转换」
func defaultOutDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents", appName)
}
