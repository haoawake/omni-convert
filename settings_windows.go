//go:build windows

package main

// 平台相关的说明文字和「关于」对话框（记住设置的代码在 app_core.go）。

// 文档页上没有 Office 时的提醒、PDF 转 Word 没有 Word 时的说明、「关于」里的说明
const (
	noOfficeBanner = "没有找到 Microsoft Office、WPS 或 LibreOffice，Word、Excel、PPT 相关的转换做不了（CSV、Markdown 转网页不受影响）。可以安装免费的 LibreOffice：zh-cn.libreoffice.org"
	noWordHint     = "没有找到 Word 或 LibreOffice，只能提取文字做成 Word 文档（版面不保留）"
	pdfToWordHint  = "" // 用目标自己的说明
	officeAbout    = "Word、Excel、PPT 的转换调用电脑上的 Microsoft Office（或 WPS、LibreOffice）。"
)

func (a *app) showAbout() {
	r, _ := (&taskDialog{title: appName, instruction: appName, content: aboutText(), icon: tdInfoIcon,
		buttons: []tdButton{{100, "打开项目主页"}, {idOK, "关闭"}}, defaultBtn: idOK}).show(a.hwnd)
	if r == 100 {
		shellOpen(repoURL)
	}
}
