//go:build windows

package main

// 记住上次用的设置：每一页选的目标和选项、保存位置。

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
)

type savedPage struct {
	Target string       `json:"target"`
	Opts   conv.Options `json:"opts"`
}

type savedSettings struct {
	Page    int                  `json:"page"`
	Pages   map[string]savedPage `json:"pages"`
	OutMode string               `json:"outMode"`
	OutDir  string               `json:"outDir"`
}

// 密码不保存
var unsavedKeys = []string{conv.OptPassword}

func (a *app) loadSettings() {
	if shotFile != "" {
		return // 截图模式用默认设置，结果才稳定
	}
	b, err := os.ReadFile(filepath.Join(settingsDir(), "settings.json"))
	if err != nil {
		return
	}
	var s savedSettings
	if json.Unmarshal(b, &s) != nil {
		return
	}
	if s.Page >= 0 && s.Page < len(a.pages) {
		a.cur = s.Page
	}
	for _, ps := range a.pages {
		sp, ok := s.Pages[ps.page.Kind.Name()]
		if !ok {
			continue
		}
		if t := catalog.ByID(sp.Target); t != nil && t.Page == ps.page.Kind {
			ps.target = t
		}
		if sp.Opts != nil {
			ps.opts = sp.Opts
		}
	}
	if s.OutMode == "custom" && s.OutDir != "" {
		a.outMode, a.outDir = "custom", s.OutDir
	} else {
		a.outDir = s.OutDir
	}
}

func (a *app) saveSettings() {
	if shotFile != "" {
		return
	}
	s := savedSettings{Page: a.cur, Pages: map[string]savedPage{}, OutMode: a.outMode, OutDir: a.outDir}
	for _, ps := range a.pages {
		o := ps.opts.Clone()
		for _, k := range unsavedKeys {
			delete(o, k)
		}
		s.Pages[ps.page.Kind.Name()] = savedPage{Target: ps.target.ID, Opts: o}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	dir := settingsDir()
	os.MkdirAll(dir, 0o755)
	tmp := filepath.Join(dir, "settings.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(dir, "settings.json"))
	}
}

func (a *app) showAbout() {
	content := "版本 " + version + "\n\n" +
		"图片、视频、音频、Word / Excel / PPT、PDF 之间的格式转换，全部在你的电脑上完成，文件不会上传到任何地方。\n\n" +
		"用到的开源组件：FFmpeg（视频和音频）、ImageMagick（图片）、PDFium 与 pdfcpu（PDF）。" +
		"Word、Excel、PPT 的转换调用电脑上的 Microsoft Office（或 WPS、LibreOffice）。"
	if info := catalog.OfficeInfo(); info != "" {
		content += "\n\n文档转换使用：" + info
	}
	r, _ := (&taskDialog{title: appName, instruction: appName, content: content, icon: tdInfoIcon,
		buttons: []tdButton{{100, "打开项目主页"}, {idOK, "关闭"}}, defaultBtn: idOK}).show(a.hwnd)
	if r == 100 {
		shellOpen(repoURL)
	}
}
