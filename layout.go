//go:build windows

package main

// 排版：根据窗口大小和当前页、目标、选项，算出每个控件的位置（a.scene），
// 摆好文件列表和选项输入框这些子窗口。画图（paint.go）和鼠标点击都用 a.scene。

import (
	"path/filepath"
	"strings"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
)

type wkind int

const (
	wText    wkind = iota
	wNav           // 左侧导航
	wChip          // 「转成 xx」按钮
	wSelect        // 下拉框
	wSeg           // 分段按钮里的一段
	wCheck         // 勾选框
	wEdit          // 输入框的外框（里面是系统的 EDIT 控件）
	wButton        // 普通按钮
	wPrimary       // 蓝色主按钮
	wDanger        // 红色按钮（停止）
	wGhost         // 无边框的小按钮
	wLink          // 文字链接
)

type widget struct {
	id      string
	kind    wkind
	r       rect
	text    string
	glyph   string
	on      bool
	enabled bool
	font    uintptr
	color   rgb
	align   uint32
	wrap    bool
	clip    rect // 非空时只在这个范围里画（选项区滚动）
	seg     int  // 分段按钮的位置：1 第一段，2 最后一段，3 只有一段
	badge   string
	click   func()
}

// 图标字体里的字形
const (
	gPhoto    = "\uEB9F"
	gVideo    = "\uE714"
	gAudio    = "\uE8D6"
	gDoc      = "\uE8A5"
	gPDF      = "\uEA90"
	gAdd      = "\uE710"
	gDelete   = "\uE74D"
	gPlay     = "\uE768"
	gStop     = "\uE71A"
	gFolder   = "\uE8B7"
	gChevron  = "\uE70D"
	gCheck    = "\uE73E"
	gInfo     = "\uE946"
	gUpload   = "\uE898"
	gMenu     = "\uE8A7"
	gWarn     = "\uE7BA"
	gTool     = "\uE90F"
	gOpenFile = "\uE8E5"
)

var pageGlyphs = []string{gPhoto, gVideo, gAudio, gDoc, gPDF}

func (a *app) measure(font uintptr, s string) int32 {
	if a.buf.dc == 0 {
		dc, _, _ := pGetDC.Call(a.hwnd)
		a.buf.ensure(dc, 1, 1)
		pReleaseDC.Call(a.hwnd, dc)
	}
	return textWidth(a.buf.dc, font, s)
}

func (a *app) measureWrap(font uintptr, s string, width int32) int32 {
	if a.buf.dc == 0 {
		a.measure(font, "")
	}
	return textHeight(a.buf.dc, font, s, width)
}

func (a *app) add(w widget) *widget {
	if w.font == 0 {
		w.font = a.f.ui
	}
	a.scene = append(a.scene, w)
	return &a.scene[len(a.scene)-1]
}

func (a *app) text(id string, r rect, s string, font uintptr, c rgb, align uint32) {
	a.add(widget{id: id, kind: wText, r: r, text: s, font: font, color: c, align: align})
}

func (a *app) btnWidth(label, glyph string) int32 {
	w := a.measure(a.f.ui, label) + a.scale(28)
	if glyph != "" {
		w += a.scale(24)
	}
	return w
}

// layout 重新排版并刷新子窗口的位置
func (a *app) layout() {
	if a.hwnd == 0 || a.s == 0 || a.lv == nil {
		return
	}
	a.scene = a.scene[:0]
	cr := clientRect(a.hwnd)
	W, H := cr.W(), cr.H()
	a.rTop = rect{0, 0, W, a.scale(56)}
	a.rNav = rect{0, a.rTop.Bottom, a.scale(212), H}
	a.rContent = rect{a.rNav.Right, a.rTop.Bottom, W, H}
	a.rAction = rect{a.rContent.Left, H - a.scale(72), W, H}

	a.layoutTop()
	a.layoutNav()
	a.layoutContent()
	a.layoutAction()
	a.layoutToast()
}

func (a *app) layoutTop() {
	r := a.rTop
	x := a.scale(20)
	title := rect{x + a.scale(34), r.Top, x + a.scale(34) + a.measure(a.f.navBold, appName) + 4, r.Bottom}
	a.text("title", title, appName, a.f.navBold, cText, dtLeft)

	right := r.Right - a.scale(16)
	bh := a.scale(32)
	y := r.Top + (r.H()-bh)/2
	label, glyph := "关于", gInfo
	bw := a.btnWidth(label, glyph) - a.scale(8)
	a.add(widget{id: "about", kind: wGhost, r: rect{right - bw, y, right, y + bh}, text: label, glyph: glyph, enabled: true, click: a.showAbout})
	right -= bw + a.scale(6)
	label, glyph = "添加到右键菜单", gMenu
	if a.menuOn {
		label, glyph = "已在右键菜单中", gCheck
	}
	bw = a.btnWidth(label, glyph) - a.scale(8)
	a.add(widget{id: "ctxmenu", kind: wGhost, r: rect{right - bw, y, right, y + bh}, text: label, glyph: glyph, on: a.menuOn, enabled: true, click: a.toggleContextMenu})
}

func (a *app) layoutNav() {
	r := a.rNav
	y := r.Top + a.scale(18)
	for i, ps := range a.pages {
		i := i
		ir := rect{r.Left + a.scale(12), y, r.Right - a.scale(12), y + a.scale(48)}
		badge := ""
		if n := len(ps.items); n > 0 {
			badge = itoa(n)
		}
		a.add(widget{id: "nav" + itoa(i), kind: wNav, r: ir, text: ps.page.Kind.Name(), glyph: pageGlyphs[i],
			on: i == a.cur, enabled: true, badge: badge, click: func() { a.switchPage(i) }})
		y += a.scale(52)
	}
	ver := "版本 " + version
	a.text("ver", rect{r.Left + a.scale(24), r.Bottom - a.scale(36), r.Right - a.scale(12), r.Bottom - a.scale(14)}, ver, a.f.small, cText3, dtLeft)
}

func (a *app) layoutContent() {
	ps := a.page()
	t := ps.target
	c := a.rContent
	padX := a.scale(28)
	x0, x1 := c.Left+padX, c.Right-padX
	y := c.Top + a.scale(22)

	// 标题和说明
	a.text("ptitle", rect{x0, y, x1, y + a.scale(32)}, ps.page.Title, a.f.title, cText, dtLeft)
	y += a.scale(34)
	sub := ps.page.Sub
	a.text("psub", rect{x0, y, x1, y + a.scale(20)}, sub, a.f.ui, cText2, dtLeft)
	y += a.scale(20) + a.scale(14)

	// 文档页：说明用哪个软件转换，没有 Office 时提醒
	if banner, warn := a.officeBanner(); banner != "" {
		h := a.measureWrap(a.f.ui, banner, x1-x0-a.scale(48)) + a.scale(16)
		col := cText2
		if warn {
			col = cWarn
		}
		a.add(widget{id: "banner", kind: wText, r: rect{x0, y, x1, y + h}, text: banner, font: a.f.ui, color: col, wrap: true, on: warn, glyph: gInfo})
		y += h + a.scale(12)
	}

	// 「转成」和「工具」两组按钮
	var formats, tools []*catalog.Target
	for _, tg := range ps.page.Targets {
		if tg.Tool {
			tools = append(tools, tg)
		} else {
			formats = append(formats, tg)
		}
	}
	chipH := a.scale(34)
	gap := a.scale(8)
	labelW := a.scale(44)
	group := func(label string, list []*catalog.Target) {
		if len(list) == 0 {
			return
		}
		a.text("g"+label, rect{x0, y, x0 + labelW, y + chipH}, label, a.f.ui, cText2, dtLeft)
		x := x0 + labelW
		for _, tg := range list {
			tg := tg
			w := a.measure(a.f.chip, tg.Label) + a.scale(26)
			w = max(w, a.scale(58))
			if x+w > x1 && x > x0+labelW {
				x = x0 + labelW
				y += chipH + gap
			}
			usable := len(ps.items) == 0 || a.acceptCount(tg) > 0
			a.add(widget{id: "chip:" + tg.ID, kind: wChip, r: rect{x, y, x + w, y + chipH}, text: tg.Label, font: a.f.chip,
				on: tg == t, enabled: true, badge: boolStr(usable), click: func() { a.chooseTarget(tg) }})
			x += w + gap
		}
		y += chipH + a.scale(10)
	}
	group("转成", formats)
	group("工具", tools)

	// 当前目标的说明
	hint := hintFor(t)
	a.add(widget{id: "hint", kind: wText, r: rect{x0, y, x1, y + a.scale(22)}, text: hint, glyph: gInfo, font: a.f.ui, color: cAccentDark, align: dtLeft})
	y += a.scale(22) + a.scale(16)

	// 下面两栏：左边设置，右边文件列表
	bottom := a.rAction.Top - a.scale(16)
	avail := x1 - x0
	optW := min(max(avail*46/100, a.scale(420)), a.scale(580))
	a.rOpts = rect{x0, y, x0 + optW, bottom}
	a.rFiles = rect{a.rOpts.Right + a.scale(20), y, x1, bottom}
	a.layoutOptions()
	a.layoutFiles()
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// ---------------------------------------------------------------- 选项区

func (a *app) layoutOptions() {
	ps := a.page()
	card := a.rOpts
	headH := a.scale(46)
	a.add(widget{id: "optstitle", kind: wText, r: rect{card.Left + a.scale(18), card.Top, card.Right - a.scale(18), card.Top + headH},
		text: "转换设置", font: a.f.uiBold, color: cText, align: dtLeft})
	body := rect{card.Left + 1, card.Top + headH, card.Right - 1, card.Bottom - 1}
	a.rOptsBody = body

	rows := ps.target.Visible(ps.opts)
	lx := body.Left + a.scale(18)
	cx := lx + a.scale(76)
	right := body.Right - a.scale(18)
	rowH := a.scale(32)
	top := body.Top + a.scale(6)
	y := top - a.optScroll
	used := map[string]bool{}
	start := len(a.scene)

	if len(rows) == 0 {
		a.text("noopts", rect{lx, y + a.scale(4), right, y + a.scale(28)}, "这个转换不需要设置，直接开始就行。", a.f.ui, cText2, dtLeft)
		y += a.scale(36)
	}
	for ri, row := range rows {
		if len(row.Fields) == 0 && row.Label == "" && ri > 0 {
			y -= a.scale(8) // 单独一行的说明紧贴着上一行
		}
		if len(row.Fields) > 0 {
			if row.Label != "" {
				a.text("rl"+itoa(ri), rect{lx, y, cx - a.scale(8), y + rowH}, row.Label, a.f.ui, cText2, dtLeft)
			}
			x := cx
			for fi, f := range row.Fields {
				w := a.fieldWidth(f)
				if x+w > right && x > cx {
					x = cx
					y += rowH + a.scale(8)
				}
				a.layoutField(f, rect{x, y, x + w, y + rowH}, ri*100+fi, used)
				x += w + a.scale(8)
				if f.Unit != "" && f.Type != catalog.Seg {
					uw := a.measure(a.f.ui, f.Unit) + 4
					a.text("u"+itoa(ri*100+fi), rect{x - a.scale(2), y, x + uw, y + rowH}, f.Unit, a.f.ui, cText2, dtLeft)
					x += uw + a.scale(8)
				}
			}
			y += rowH
		}
		if row.Note != "" {
			nx := cx
			if len(row.Fields) == 0 && row.Label == "" {
				nx = cx
			}
			h := a.measureWrap(a.f.small, row.Note, right-nx)
			y += a.scale(4)
			a.add(widget{id: "rn" + itoa(ri), kind: wText, r: rect{nx, y, right, y + h}, text: row.Note, font: a.f.small, color: cText3, wrap: true})
			y += h
		}
		y += a.scale(12)
	}
	a.optHeight = y + a.optScroll - top + a.scale(6)
	if max(0, a.optHeight-body.H()) < a.optScroll {
		a.optScroll = max(0, a.optHeight-body.H())
		a.scene = a.scene[:start]
		a.layoutOptionsAgain()
		return
	}
	for i := start; i < len(a.scene); i++ {
		a.scene[i].clip = body
	}
	// 用不到的输入框藏起来
	for k, h := range a.edits {
		if !used[k] {
			pShowWindow.Call(h, 0)
		}
	}
}

// layoutOptionsAgain 滚动位置被截短后重排一次（窗口变大时）
func (a *app) layoutOptionsAgain() { a.layoutOptions() }

func (a *app) fieldWidth(f catalog.Field) int32 {
	switch f.Type {
	case catalog.Seg:
		var w int32
		for _, c := range f.Choices {
			w += a.measure(a.f.ui, c.Label) + a.scale(22)
		}
		return w
	case catalog.Check:
		return a.scale(26) + a.measure(a.f.ui, f.Label)
	case catalog.Static:
		return a.measure(a.f.ui, f.Label) + a.scale(2)
	}
	w := int32(f.Width)
	if w == 0 {
		w = 120
	}
	return a.scale(float64(w))
}

func (a *app) layoutField(f catalog.Field, r rect, n int, used map[string]bool) {
	ps := a.page()
	v := ps.opts[f.Key]
	visible := r.Top >= a.rOptsBody.Top && r.Bottom <= a.rOptsBody.Bottom
	switch f.Type {
	case catalog.Static:
		a.text("st"+itoa(n), r, f.Label, a.f.ui, cText2, dtCenter)
	case catalog.Select:
		label := ""
		for _, c := range f.Choices {
			if c.Value == v {
				label = c.Label
			}
		}
		if f.Apply != nil || label == "" {
			label = f.Choices[0].Label
		}
		a.add(widget{id: "sel:" + f.Key, kind: wSelect, r: r, text: label, enabled: true,
			click: func() { a.pickChoice(f, r) }})
	case catalog.Seg:
		x := r.Left
		for i, c := range f.Choices {
			c := c
			w := a.measure(a.f.ui, c.Label) + a.scale(22)
			pos := 0
			if i == 0 {
				pos |= 1
			}
			if i == len(f.Choices)-1 {
				pos |= 2
			}
			a.add(widget{id: "seg:" + f.Key + ":" + c.Value, kind: wSeg, r: rect{x, r.Top, x + w, r.Bottom}, text: c.Label,
				on: v == c.Value, enabled: true, seg: pos, click: func() { a.setOpt(f.Key, c.Value) }})
			x += w
		}
	case catalog.Check:
		on := conv.Options{f.Key: v}.Bool(f.Key)
		a.add(widget{id: "chk:" + f.Key, kind: wCheck, r: r, text: f.Label, on: on, enabled: true,
			click: func() {
				nv := "1"
				if on {
					nv = "0"
				}
				a.setOpt(f.Key, nv)
			}})
	case catalog.Number, catalog.Text, catalog.Password:
		a.add(widget{id: "edit:" + f.Key, kind: wEdit, r: r, enabled: true})
		h := a.editFor(f)
		a.setCue(h, f.Placeholder)
		used[f.Key] = true
		if !visible {
			pShowWindow.Call(h, 0)
			return
		}
		eh := a.scale(20)
		pMoveWindow.Call(h, uintptr(r.Left+a.scale(9)), uintptr(r.Top+(r.H()-eh)/2), uintptr(r.W()-a.scale(18)), uintptr(eh), 1)
		pShowWindow.Call(h, 8 /* SW_SHOWNA */)
	}
}

// ---------------------------------------------------------------- 文件列表

func (a *app) layoutFiles() {
	ps := a.page()
	card := a.rFiles
	headH := a.scale(46)
	title := "文件列表"
	a.text("ftitle", rect{card.Left + a.scale(18), card.Top, card.Left + a.scale(18) + a.measure(a.f.uiBold, title) + 2, card.Top + headH}, title, a.f.uiBold, cText, dtLeft)
	if n := len(ps.items); n > 0 {
		sub := itoa(n) + " 个文件"
		if k := a.acceptCount(ps.target); k < n {
			sub += "，其中 " + itoa(k) + " 个能转成「" + ps.target.Label + "」"
		}
		x := card.Left + a.scale(26) + a.measure(a.f.uiBold, title)
		a.text("fcount", rect{x, card.Top, card.Right - a.scale(220), card.Top + headH}, sub, a.f.ui, cText3, dtLeft)
	}
	bh := a.scale(30)
	by := card.Top + (headH-bh)/2
	right := card.Right - a.scale(12)
	if len(ps.items) > 0 {
		w := a.btnWidth("清空", gDelete) - a.scale(10)
		a.add(widget{id: "clear", kind: wGhost, r: rect{right - w, by, right, by + bh}, text: "清空", glyph: gDelete, enabled: true, click: a.clearList})
		right -= w + a.scale(4)
		w = a.btnWidth("添加文件", gAdd) - a.scale(10)
		a.add(widget{id: "addfiles", kind: wGhost, r: rect{right - w, by, right, by + bh}, text: "添加文件", glyph: gAdd, enabled: true, click: a.addFilesDialog})
	}
	a.rList = rect{card.Left + a.scale(10), card.Top + headH + a.scale(4), card.Right - a.scale(6), card.Bottom - a.scale(10)}
	if len(ps.items) == 0 {
		pShowWindow.Call(a.lv.hwnd, 0)
		a.layoutDropZone()
		return
	}
	pMoveWindow.Call(a.lv.hwnd, uintptr(a.rList.Left), uintptr(a.rList.Top), uintptr(a.rList.W()), uintptr(a.rList.H()), 1)
	a.lv.fitColumns(a.rList.W())
	pShowWindow.Call(a.lv.hwnd, swShow)
}

func (a *app) layoutDropZone() {
	r := a.rList
	zone := rect{r.Left + a.scale(16), r.Top + a.scale(4), r.Right - a.scale(16), r.Bottom - a.scale(8)}
	a.add(widget{id: "dropzone", kind: wText, r: zone, text: ""})
	cy := zone.Top + zone.H()/2 - a.scale(70)
	a.text("dzicon", rect{zone.Left, cy, zone.Right, cy + a.scale(56)}, gUpload, a.f.iconBig, cAccent, dtCenter)
	cy += a.scale(64)
	a.text("dz1", rect{zone.Left, cy, zone.Right, cy + a.scale(24)}, "把文件或文件夹拖到这里", a.f.navBold, cText, dtCenter)
	cy += a.scale(28)
	a.text("dz2", rect{zone.Left + a.scale(20), cy, zone.Right - a.scale(20), cy + a.scale(20)}, "也可以在资源管理器里复制文件，回到这里按 Ctrl+V", a.f.ui, cText3, dtCenter)
	cy += a.scale(34)
	bw := a.btnWidth("添加文件", gAdd)
	bx := zone.Left + (zone.W()-bw)/2
	a.add(widget{id: "dzadd", kind: wPrimary, r: rect{bx, cy, bx + bw, cy + a.scale(38)}, text: "添加文件", glyph: gAdd, enabled: true, click: a.addFilesDialog})
}

// ---------------------------------------------------------------- 底部操作栏

func (a *app) layoutAction() {
	r := a.rAction
	x0 := r.Left + a.scale(28)
	x1 := r.Right - a.scale(28)
	bh := a.scale(42)
	by := r.Top + (r.H()-bh)/2

	busy := a.pageBusy(a.page())
	a.btnBusy = busy
	label, glyph, kind := "开始转换", gPlay, wPrimary
	if busy {
		label, glyph, kind = "停止", gStop, wDanger
	}
	bw := max(a.btnWidth(label, glyph)+a.scale(20), a.scale(150))
	a.add(widget{id: "start", kind: kind, r: rect{x1 - bw, by, x1, by + bh}, text: label, glyph: glyph, enabled: true, font: a.f.uiBold, click: a.startOrStop})

	a.text("savelbl", rect{x0, r.Top, x0 + a.scale(52), r.Bottom}, "保存到", a.f.ui, cText2, dtLeft)
	dest := "和原文件放在同一个文件夹"
	if a.outMode == "custom" && a.outDir != "" {
		dest = shortPath(a.outDir, 48)
	}
	sw := min(max(a.measure(a.f.ui, dest)+a.scale(80), a.scale(240)), a.scale(460))
	sx := x0 + a.scale(56)
	sh := a.scale(34)
	sy := r.Top + (r.H()-sh)/2
	a.add(widget{id: "outdir", kind: wSelect, r: rect{sx, sy, sx + sw, sy + sh}, text: dest, glyph: gFolder, enabled: true, click: a.chooseOutDir})

	// 进度
	if a.sess != nil {
		a.add(widget{id: "status", kind: wText, r: rect{sx + sw + a.scale(20), r.Top, x1 - bw - a.scale(16), r.Bottom}, font: a.f.ui, color: cText2, align: dtRight})
	}
}

// ---------------------------------------------------------------- 提示条

func (a *app) layoutToast() {
	if a.toast == "" {
		return
	}
	// 放在底栏中间（「保存到」和「开始转换」之间）：这里没有子窗口，不会被文件列表盖住
	var left, right int32
	for _, w := range a.scene {
		switch w.id {
		case "outdir":
			left = w.r.Right + a.scale(20)
		case "start":
			right = w.r.Left - a.scale(16)
		}
	}
	action := a.toastAction != ""
	actW := int32(0)
	if action {
		actW = a.measure(a.f.uiBold, "打开文件夹") + a.scale(28)
	}
	w := min(a.measure(a.f.ui, a.toast)+a.scale(44)+actW, right-left)
	if w < a.scale(120) {
		return
	}
	h := a.scale(40)
	x := right - w
	y := a.rAction.Top + (a.rAction.H()-h)/2
	a.add(widget{id: "toast", kind: wText, r: rect{x, y, x + w, y + h}, text: a.toast, on: a.toastErr})
	if action {
		ar := rect{x + w - actW - a.scale(6), y + a.scale(5), x + w - a.scale(6), y + h - a.scale(5)}
		target := a.toastAction
		a.add(widget{id: "toastact", kind: wLink, r: ar, text: "打开文件夹", enabled: true, click: func() {
			revealFile(target)
		}})
	}
}

// shortPath 把很长的路径缩成「D:\…\文件夹」的样子
func shortPath(p string, n int) string {
	if len([]rune(p)) <= n {
		return p
	}
	vol := filepath.VolumeName(p)
	parts := strings.Split(strings.TrimPrefix(p, vol), `\`)
	if len(parts) > 2 {
		return vol + `\…\` + parts[len(parts)-2] + `\` + parts[len(parts)-1]
	}
	return p
}
