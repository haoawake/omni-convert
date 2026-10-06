package office

// macOS：用 LibreOffice 转换 Word、Excel、PPT（命令行无界面运行，和 Windows 上没装 Office 时一样）。
// Microsoft Office for Mac 只能用 AppleScript 遥控，每次打开文件都要用户点「授予访问权限」，没法在后台转换，所以不用它。
// 网页、Markdown、TXT 转 PDF 用 Edge 或 Chrome 的无界面模式打印，都没有时交给 LibreOffice。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
)

// LibreOfficeURL 是 LibreOffice 的下载页
const LibreOfficeURL = "https://zh-cn.libreoffice.org/download/libreoffice/"

func detectSystem() (Info, [numApps]progInfo) {
	return Info{LibreOffice: findSoffice(), Edge: findBrowser()}, [numApps]progInfo{}
}

// appDirs 是放应用程序的地方：「应用程序」和用户自己的「应用程序」
func appDirs() []string {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
}

func findSoffice() string {
	for _, d := range appDirs() {
		if p := filepath.Join(d, "LibreOffice.app", "Contents", "MacOS", "soffice"); fileExists(p) {
			return p
		}
	}
	// 放在别的地方（比如还在「下载」里）的 LibreOffice：问一下聚焦搜索
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "/usr/bin/mdfind", "kMDItemCFBundleIdentifier == 'org.libreoffice.script'").Output()
	for _, app := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p := filepath.Join(app, "Contents", "MacOS", "soffice"); app != "" && !strings.Contains(app, "/Volumes/") && fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("soffice"); err == nil {
		return p
	}
	return ""
}

// findBrowser 找能把网页打印成 PDF 的浏览器：Edge、Chrome、Chromium
func findBrowser() string {
	for _, app := range []string{"Microsoft Edge", "Google Chrome", "Chromium"} {
		for _, d := range appDirs() {
			if p := filepath.Join(d, app+".app", "Contents", "MacOS", app); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func newCOMEngine(appKind) engine { return nil }

func shutdownCOM() {}

func errNoOffice() error {
	return conv.Fail("需要安装免费的 LibreOffice 才能转换 Word、Excel、PPT 文件",
		"下载地址："+LibreOfficeURL+"\n装好后回到万能格式转换再点「开始转换」。Mac 上的 Microsoft Office 不能在后台自动转换，所以用的是 LibreOffice。")
}
