//go:build darwin

package main

// macOS 的主窗口：和 Windows 版一样的结构（左边分类，上面「转成」，左下转换设置，右边文件列表，底栏保存位置和
// 「开始转换」），用的是系统自带的 AppKit 控件，深色模式、视网膜屏都由系统处理。
// 控件的创建和事件在 ui_darwin.m；这里决定摆什么、摆在哪，以及点了之后做什么。
// 界面状态（每页的文件、设置、转换任务）和 Windows 共用，见 app_core.go。

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=12.0 -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework UniformTypeIdentifiers -mmacosx-version-min=12.0
#include <stdlib.h>
#include "ui_darwin.h"
*/
import "C"

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/office"
)

// ui_post 的种类
const (
	postUpdate = 1 // 任务状态变了
	postFound  = 2 // 后台展开文件夹找到了文件
	postLayout = 3 // 显卡检测完了、找到了 LibreOffice……重新排版
)

// 定时器
const (
	timerBusy  = 1 // 有任务在跑：刷新进度
	timerToast = 2
)

// 菜单命令（和 ui_darwin.m 里的 M_XXX 一致）
const (
	menuAbout = iota + 1
	menuAdd
	menuStart
	menuStop
	menuRemove
	menuClear
	menuReveal
	menuHome
	menuLibreOffice
	menuPage = 100
)

// 文件列表右键菜单
const (
	cmOpen = iota + 1
	cmReveal
	cmOpenOut
	cmRevealOut
	cmError
	cmCopyErr
	cmUp
	cmDown
	cmStop
	cmRemove
	cmRedo
)

const (
	privacyURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_FilesAndFolders"
	sideW      = 200.0 // 左边分类的宽度（和 ui_darwin.m 里一致）
)

var pageSymbols = []string{"photo", "film", "music.note", "doc.text", "doc.richtext"}

type macApp struct {
	core

	ready   bool
	views   map[string]C.int // 按名字记住的控件
	vkind   map[string]C.int
	vparent map[string]C.int
	used    map[string]bool // 这一次排版用到的控件
	actions map[C.int]func(v int)
	edits   map[C.int]string // 输入框 → 选项的键

	optScroll C.int
	list      C.int
	btnBusy   bool

	toast       string
	toastErr    bool
	toastAction string
	pending     []string // 窗口建好之前收到的文件（从访达「打开方式」启动时）
	quitOK      bool
	explained   map[string]bool // 已经解释过「没有权限」的文件夹

	dirty   atomic.Bool
	walking atomic.Int32
	foundMu sync.Mutex
	found   []foundFiles
}

var mac *macApp

func runApp(initial []string) {
	a := &macApp{views: map[string]C.int{}, vkind: map[string]C.int{}, vparent: map[string]C.int{},
		actions: map[C.int]func(int){}, edits: map[C.int]string{}, explained: map[string]bool{}}
	mac = a
	a.pending = initial
	a.initCore(func() {
		if !a.dirty.Swap(true) {
			C.ui_post(postUpdate)
		}
	})
	C.ui_init()
	if shotFile != "" {
		C.ui_dark(boolInt(os.Getenv("OMNI_SHOT_APPEARANCE") == "dark"))
	}
	withC(appName, func(s *C.char) { C.ui_window(s, 1260, 820, 1080, 700) })
	withC("版本 "+version, func(s *C.char) { C.ui_set_version(s) })
	a.ready = true
	a.refreshSidebar()
	a.layout()
	go office.Detect() // 先在后台找一下 LibreOffice，用到时就不用等
	catalog.DetectGPU(func() { C.ui_post(postLayout) })
	C.ui_run()
}

// ---------------------------------------------------------------- 小工具

func withC(s string, f func(*C.char)) {
	c := C.CString(s)
	defer C.free(unsafe.Pointer(c))
	f(c)
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func goLines(p *C.char) []string {
	if p == nil {
		return nil
	}
	s := C.GoString(p)
	C.ui_free(p)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func measure(s string, font C.int, maxW float64) (float64, float64) {
	var w, h C.double
	withC(s, func(c *C.char) { C.ui_measure(c, font, C.double(maxW), &w, &h) })
	return float64(w), float64(h)
}

func fit(id C.int) (float64, float64) {
	var w, h C.double
	C.ui_fit(id, &w, &h)
	return float64(w), float64(h)
}

func setText(id C.int, s string) { withC(s, func(c *C.char) { C.ui_text(id, c) }) }

func setFrame(id C.int, x, y, w, h float64) {
	C.ui_frame(id, C.double(x), C.double(y), C.double(w), C.double(h))
}

func alert(style int, title, msg, detail string, buttons ...string) int {
	var r C.int
	withC(title, func(t *C.char) {
		withC(msg, func(m *C.char) {
			withC(detail, func(d *C.char) {
				withC(strings.Join(buttons, "\n"), func(b *C.char) { r = C.ui_alert(C.int(style), t, m, d, b) })
			})
		})
	})
	return int(r)
}

func openPath(p string) { withC(p, func(c *C.char) { C.ui_open_path(c) }) }
func reveal(p string)   { withC(p, func(c *C.char) { C.ui_reveal(c) }) }
func openURL(u string)  { withC(u, func(c *C.char) { C.ui_open_url(c) }) }

// w 取出（或者新建）名字叫 key 的控件，并记下这一次排版用到了它
func (a *macApp) w(key string, kind C.int, parent C.int) C.int {
	a.used[key] = true
	if id, ok := a.views[key]; ok {
		if a.vkind[key] == kind && a.vparent[key] == parent {
			return id
		}
		a.drop(key)
	}
	id := C.ui_new(kind, parent)
	a.views[key], a.vkind[key], a.vparent[key] = id, kind, parent
	return id
}

func (a *macApp) drop(key string) {
	id := a.views[key]
	C.ui_remove(id)
	delete(a.views, key)
	delete(a.vkind, key)
	delete(a.vparent, key)
	delete(a.actions, id)
	delete(a.edits, id)
}

// label 摆一行（或几行）文字，返回它的高度
func (a *macApp) label(key string, parent C.int, s string, font, tone C.int, x, y, width float64, wrap bool) float64 {
	kind := C.int(C.UI_LABEL)
	if wrap {
		kind = C.UI_WRAP
	}
	id := a.w(key, kind, parent)
	setText(id, s)
	C.ui_font(id, font)
	C.ui_tone(id, tone)
	_, h := measure(s, font, width-6) // 文字框左右各有一点留白
	if !wrap {
		_, h = measure("国", font, 0)
	}
	setFrame(id, x, y, width, h+2)
	return h + 2
}

// button 摆一个按钮（在 y 开始、高 rowH 的这一行里竖直居中；rightAlign 时 x 是右边缘），返回它的宽度
func (a *macApp) button(key string, parent, kind C.int, title, symbol string, x, y, rowH, minW float64, rightAlign bool, click func()) float64 {
	id := a.w(key, kind, parent)
	setText(id, title)
	if symbol != "" {
		withC(symbol, func(c *C.char) { C.ui_symbol(id, c, 13) })
	}
	bw, bh := fit(id)
	bw = max(bw, minW)
	if rightAlign {
		x -= bw
	}
	setFrame(id, x, y+(rowH-bh)/2, bw, bh)
	a.actions[id] = func(int) { click() }
	return bw
}

// ---------------------------------------------------------------- 排版

func (a *macApp) refreshSidebar() {
	var titles, badges []string
	for _, ps := range a.pages {
		titles = append(titles, ps.page.Kind.Name())
		b := ""
		if n := len(ps.items); n > 0 {
			b = itoa(n)
		}
		badges = append(badges, b)
	}
	withC(strings.Join(titles, "\n"), func(t *C.char) {
		withC(strings.Join(pageSymbols, "\n"), func(s *C.char) {
			withC(strings.Join(badges, "\n"), func(b *C.char) { C.ui_sidebar(t, s, b, C.int(a.cur)) })
		})
	})
}

func (a *macApp) layout() {
	if !a.ready {
		return
	}
	a.used = map[string]bool{}
	var cw, ch C.double
	C.ui_content_size(&cw, &ch)
	W, H := float64(cw), float64(ch)
	ps := a.page()
	t := ps.target
	x0, x1 := sideW+28, W-28
	top := max(float64(C.ui_top_inset()), 28) + 6

	// 标题和说明
	y := top
	y += a.label("ptitle", 0, ps.page.Title, C.UI_FONT_TITLE, C.UI_TONE_TEXT, x0, y, x1-x0, false) + 4
	y += a.label("psub", 0, ps.page.Sub, C.UI_FONT_BODY, C.UI_TONE_TEXT2, x0, y, x1-x0, true) + 14

	// 文档页：没装 LibreOffice 时提醒
	if banner, warn := a.officeBanner(); banner != "" && warn {
		bid := a.w("banner", C.UI_BANNER, 0)
		textW := x1 - x0 - 44 - 150
		_, th := measure(banner, C.UI_FONT_BODY, textW)
		bh := max(th+20, 44)
		setFrame(bid, x0, y, x1-x0, bh)
		ic := a.w("bannericon", C.UI_IMAGE, 0)
		withC("exclamationmark.triangle.fill", func(c *C.char) { C.ui_symbol(ic, c, 16) })
		C.ui_tone(ic, C.UI_TONE_WARN)
		setFrame(ic, x0+12, y+(bh-18)/2, 20, 18)
		a.label("bannertext", 0, banner, C.UI_FONT_BODY, C.UI_TONE_TEXT, x0+40, y+(bh-th)/2-1, textW, true)
		a.button("bannerbtn", 0, C.UI_BUTTON, "下载 LibreOffice", "arrow.down.circle", x1-12, y, bh, 0, true,
			func() { openURL(office.LibreOfficeURL) })
		y += bh + 14
	}

	// 「转成」和「工具」两组按钮
	var formats, toolsList []*catalog.Target
	for _, tg := range ps.page.Targets {
		if tg.Tool {
			toolsList = append(toolsList, tg)
		} else {
			formats = append(formats, tg)
		}
	}
	const chipGap, rowGap = 8.0, 8.0
	labelW := 44.0
	group := func(name string, list []*catalog.Target) {
		if len(list) == 0 {
			return
		}
		chipH := 0.0
		x := x0 + labelW
		rowY := y
		for _, tg := range list {
			tg := tg
			id := a.w("chip:"+tg.ID, C.UI_CHIP, 0)
			setText(id, tg.Label)
			C.ui_on(id, boolInt(tg == t))
			usable := len(ps.items) == 0 || a.acceptCount(tg) > 0
			C.ui_enabled(id, 1)
			tip := tg.Hint
			if !usable {
				tip = "列表里没有能转成「" + tg.Label + "」的文件。" + tg.Hint
			}
			withC(tip, func(c *C.char) { C.ui_tooltip(id, c) })
			bw, bh := fit(id)
			bw = max(bw, 58)
			chipH = bh
			if x+bw > x1 && x > x0+labelW {
				x = x0 + labelW
				rowY += bh + rowGap
			}
			setFrame(id, x, rowY, bw, bh)
			a.actions[id] = func(int) { a.chooseTarget(tg) }
			x += bw + chipGap
		}
		_, lh := measure(name, C.UI_FONT_BODY, 0)
		a.label("g"+name, 0, name, C.UI_FONT_BODY, C.UI_TONE_TEXT2, x0, y+(chipH-lh)/2-1, labelW, false)
		y = rowY + chipH + 10
	}
	group("转成", formats)
	group("工具", toolsList)

	// 当前目标的说明
	hint := hintFor(t)
	ic := a.w("hinticon", C.UI_IMAGE, 0)
	withC("info.circle", func(c *C.char) { C.ui_symbol(ic, c, 13) })
	C.ui_tone(ic, C.UI_TONE_ACCENT)
	setFrame(ic, x0, y+1, 16, 16)
	hh := a.label("hint", 0, hint, C.UI_FONT_BODY, C.UI_TONE_ACCENT, x0+22, y, x1-x0-22, true)
	y += max(hh, 18) + 16

	// 下面两栏：左边设置，右边文件列表；最底下是操作栏
	barH := 64.0
	bottom := H - barH - 14
	avail := x1 - x0
	optW := min(max(avail*0.42, 400), 520)
	a.layoutOptions(x0, y, optW, bottom-y)
	a.layoutFiles(x0+optW+18, y, x1-(x0+optW+18), bottom-y)
	a.layoutBar(x0, x1, H-barH, barH)

	for k := range a.views {
		if !a.used[k] {
			a.drop(k)
		}
	}
}

// ---------------------------------------------------------------- 转换设置

func (a *macApp) layoutOptions(x, y, w, h float64) {
	ps := a.page()
	card := a.w("optcard", C.UI_CARD, 0)
	setFrame(card, x, y, w, h)
	a.label("opttitle", 0, "转换设置", C.UI_FONT_BOLD, C.UI_TONE_TEXT, x+16, y+12, w-32, false)
	line := a.w("optline", C.UI_LINE, 0)
	setFrame(line, x+1, y+40, w-2, 1)
	sc := a.w("optscroll", C.UI_SCROLL, 0)
	a.optScroll = sc
	setFrame(sc, x+1, y+41, w-2, h-42)
	dw := float64(C.ui_doc_width(sc))
	if dw <= 0 {
		dw = w - 2
	}

	rows := ps.target.Visible(ps.opts)
	lx, lw := 14.0, 62.0
	cx := lx + lw + 10
	right := dw - 16
	const rowH = 28.0
	yy := 12.0
	if len(rows) == 0 {
		yy += a.label("noopts", sc, "这个转换不需要设置，直接开始就行。", C.UI_FONT_BODY, C.UI_TONE_TEXT2, lx, yy+4, right-lx, true) + 8
	}
	for ri, row := range rows {
		if len(row.Fields) == 0 && row.Label == "" && ri > 0 {
			yy -= 6 // 单独一行的说明紧贴着上一行
		}
		if len(row.Fields) > 0 {
			if row.Label != "" {
				_, lh := measure(row.Label, C.UI_FONT_BODY, 0)
				id := a.w("rl"+itoa(ri), C.UI_LABEL, sc)
				setText(id, row.Label)
				C.ui_tone(id, C.UI_TONE_TEXT2)
				setFrame(id, lx-6, yy+(rowH-lh)/2-1, lw+6, lh+2)
				C.ui_align(id, 2)
			}
			xx := cx
			for fi, f := range row.Fields {
				key := itoa(ri*100 + fi)
				fw := a.fieldWidth(f, sc, key)
				need := fw
				if f.Unit != "" && f.Type != catalog.Seg {
					uw, _ := measure(f.Unit, C.UI_FONT_BODY, 0)
					need += uw + 8 // 单位和输入框放在同一行
				}
				if xx+need > right && xx > cx {
					xx = cx
					yy += rowH + 8
				}
				a.layoutField(f, sc, key, xx, yy, fw, rowH)
				xx += fw + 8
				if f.Unit != "" && f.Type != catalog.Seg {
					uw, uh := measure(f.Unit, C.UI_FONT_BODY, 0)
					id := a.w("u"+key, C.UI_LABEL, sc)
					setText(id, f.Unit)
					C.ui_tone(id, C.UI_TONE_TEXT2)
					setFrame(id, xx-2, yy+(rowH-uh)/2-1, uw+8, uh+2)
					xx += uw + 8
				}
			}
			yy += rowH
		}
		if row.Note != "" {
			yy += 3
			yy += a.label("rn"+itoa(ri), sc, row.Note, C.UI_FONT_SMALL, C.UI_TONE_TEXT3, cx, yy, right-cx, true)
		}
		yy += 12
	}
	C.ui_doc_height(sc, C.double(yy+8))
}

// fieldWidth 是选项控件的宽度（下拉框、分段按钮按内容算）
func (a *macApp) fieldWidth(f catalog.Field, parent C.int, key string) float64 {
	switch f.Type {
	case catalog.Static:
		w, _ := measure(f.Label, C.UI_FONT_BODY, 0)
		return w + 6
	case catalog.Check:
		id := a.fieldView(f, parent, key)
		w, _ := fit(id)
		return w
	case catalog.Seg:
		id := a.fieldView(f, parent, key)
		w, _ := fit(id)
		return w
	case catalog.Select:
		id := a.fieldView(f, parent, key)
		w, _ := fit(id)
		return max(w, float64(f.Width)+16)
	}
	w := float64(f.Width)
	if w == 0 {
		w = 120
	}
	return w + 6
}

// fieldView 建好选项控件并填上内容（宽度要按内容算，所以先建）
func (a *macApp) fieldView(f catalog.Field, parent C.int, key string) C.int {
	ps := a.page()
	v := ps.opts[f.Key]
	switch f.Type {
	case catalog.Select:
		kind := C.int(C.UI_POPUP)
		if f.Apply != nil {
			kind = C.UI_PULLDOWN
		}
		id := a.w("sel:"+f.Key, kind, parent)
		var labels []string
		sel := 0
		for i, c := range f.Choices {
			labels = append(labels, c.Label)
			if c.Value == v {
				sel = i
			}
		}
		withC(strings.Join(labels, "\n"), func(c *C.char) { C.ui_items(id, c, C.int(sel)) })
		a.actions[id] = func(i int) {
			if i < 0 || i >= len(f.Choices) {
				return
			}
			c := f.Choices[i]
			if f.Apply != nil {
				if c.Value != "" {
					f.Apply(a.page().opts, c.Value)
				}
			} else {
				a.page().opts[f.Key] = c.Value
			}
			a.layout()
		}
		return id
	case catalog.Seg:
		id := a.w("seg:"+f.Key, C.UI_SEGMENT, parent)
		var labels []string
		sel := -1
		for i, c := range f.Choices {
			labels = append(labels, c.Label)
			if c.Value == v {
				sel = i
			}
		}
		withC(strings.Join(labels, "\n"), func(c *C.char) { C.ui_items(id, c, C.int(sel)) })
		a.actions[id] = func(i int) {
			if i >= 0 && i < len(f.Choices) {
				a.page().opts[f.Key] = f.Choices[i].Value
				a.layout()
			}
		}
		return id
	case catalog.Check:
		id := a.w("chk:"+f.Key, C.UI_CHECK, parent)
		setText(id, f.Label)
		C.ui_on(id, boolInt(conv.Options{f.Key: v}.Bool(f.Key)))
		a.actions[id] = func(on int) {
			a.page().opts[f.Key] = map[bool]string{true: "1", false: "0"}[on != 0]
			a.layout()
		}
		return id
	}
	return 0
}

func (a *macApp) layoutField(f catalog.Field, parent C.int, key string, x, y, w, rowH float64) {
	ps := a.page()
	switch f.Type {
	case catalog.Static:
		_, lh := measure(f.Label, C.UI_FONT_BODY, 0)
		id := a.w("st"+key, C.UI_LABEL, parent)
		setText(id, f.Label)
		C.ui_tone(id, C.UI_TONE_TEXT2)
		setFrame(id, x, y+(rowH-lh)/2-1, w, lh+2)
	case catalog.Select, catalog.Seg, catalog.Check:
		id := a.fieldView(f, parent, key)
		_, h := fit(id)
		setFrame(id, x, y+(rowH-h)/2, w, h)
	case catalog.Number, catalog.Text, catalog.Password:
		kind := C.int(C.UI_TEXT)
		switch f.Type {
		case catalog.Number:
			kind = C.UI_NUMBER
		case catalog.Password:
			kind = C.UI_SECURE
		}
		id := a.w("edit:"+f.Key, kind, parent)
		a.edits[id] = f.Key
		setText(id, ps.opts[f.Key])
		withC(f.Placeholder, func(c *C.char) { C.ui_placeholder(id, c) })
		h := 22.0
		setFrame(id, x, y+(rowH-h)/2, w, h)
	}
}

// ---------------------------------------------------------------- 文件列表

func (a *macApp) layoutFiles(x, y, w, h float64) {
	ps := a.page()
	card := a.w("filecard", C.UI_CARD, 0)
	setFrame(card, x, y, w, h)
	if a.list == 0 {
		a.list = C.ui_new(C.UI_LIST, 0) // 要在卡片之后建，才不会被卡片挡住
	}
	title := "文件列表"
	tw, _ := measure(title, C.UI_FONT_BOLD, 0)
	a.label("ftitle", 0, title, C.UI_FONT_BOLD, C.UI_TONE_TEXT, x+16, y+12, tw+8, false)
	right := x + w - 10
	if n := len(ps.items); n > 0 {
		sub := itoa(n) + " 个文件"
		if k := a.acceptCount(ps.target); k < n {
			sub += "，其中 " + itoa(k) + " 个能转成「" + ps.target.Label + "」"
		}
		bw := a.button("clear", 0, C.UI_LINK, "清空", "trash", right, y, 40, 0, true, a.clearList)
		right -= bw + 14
		bw = a.button("addfiles", 0, C.UI_LINK, "添加文件…", "plus", right, y, 40, 0, true, a.addFilesDialog)
		right -= bw + 14
		a.label("fcount", 0, sub, C.UI_FONT_BODY, C.UI_TONE_TEXT3, x+16+tw+10, y+12, max(right-(x+16+tw+10), 40), false)
	}
	line := a.w("fileline", C.UI_LINE, 0)
	setFrame(line, x+1, y+40, w-2, 1)

	if len(ps.items) == 0 {
		C.ui_hidden(a.list, 1)
		a.layoutDropZone(x+16, y+56, w-32, h-72)
		return
	}
	C.ui_hidden(a.list, 0)
	setFrame(a.list, x+1, y+41, w-2, h-42-6)
}

func (a *macApp) layoutDropZone(x, y, w, h float64) {
	dz := a.w("dropzone", C.UI_DROPZONE, 0)
	setFrame(dz, x, y, w, h)
	cy := y + h/2 - 84
	ic := a.w("dzicon", C.UI_IMAGE, 0)
	withC("tray.and.arrow.down", func(c *C.char) { C.ui_symbol(ic, c, 40) })
	C.ui_tone(ic, C.UI_TONE_ACCENT)
	setFrame(ic, x+w/2-30, cy, 60, 52)
	cy += 64
	center := func(key, s string, font, tone C.int, gap float64) {
		_, th := measure(s, font, w-44)
		id := a.w(key, C.UI_WRAP, 0)
		setText(id, s)
		C.ui_font(id, font)
		C.ui_tone(id, tone)
		C.ui_align(id, 1)
		setFrame(id, x+20, cy, w-40, th+2)
		cy += th + gap
	}
	center("dz1", "把文件或文件夹拖到这里", C.UI_FONT_HEADLINE, C.UI_TONE_TEXT, 8)
	center("dz2", "也可以在访达里拷贝文件，回到这里按 ⌘V", C.UI_FONT_BODY, C.UI_TONE_TEXT3, 18)
	id := a.w("dzadd", C.UI_PRIMARY, 0)
	setText(id, "添加文件…")
	bw, bh := fit(id)
	bw = max(bw, 130)
	setFrame(id, x+(w-bw)/2, cy, bw, bh)
	a.actions[id] = func(int) { a.addFilesDialog() }
}

// ---------------------------------------------------------------- 底栏

func (a *macApp) layoutBar(x0, x1, y, h float64) {
	line := a.w("barline", C.UI_LINE, 0)
	setFrame(line, sideW, y, x1+28-sideW, 1)

	busy := a.pageBusy(a.page())
	a.btnBusy = busy
	label, symbol, kind := " 开始转换", "play.fill", C.int(C.UI_PRIMARY)
	if busy {
		label, symbol, kind = " 停止", "stop.fill", C.UI_DANGER
	}
	sid := a.w("start", kind, 0)
	setText(sid, label)
	withC(symbol, func(c *C.char) { C.ui_symbol(sid, c, 12) })
	withC("快捷键 ⌘↩", func(c *C.char) { C.ui_tooltip(sid, c) })
	bw, bh := fit(sid)
	bw = max(bw+24, 150)
	setFrame(sid, x1-bw, y+(h-bh)/2, bw, bh)
	a.actions[sid] = func(int) { a.startOrStop() }

	_, lh := measure("保存到", C.UI_FONT_BODY, 0)
	a.label("savelbl", 0, "保存到", C.UI_FONT_BODY, C.UI_TONE_TEXT2, x0, y+(h-lh)/2-1, 54, false)
	pid := a.w("outdir", C.UI_POPUP, 0)
	items := []string{"和原文件放在同一个文件夹"}
	sel := 0
	if a.outDir != "" {
		items = append(items, shortHome(a.outDir))
		if a.outMode == "custom" {
			sel = 1
		}
	}
	items = append(items, "-", "选择文件夹…")
	if a.outMode == "custom" && a.outDir != "" {
		items = append(items, "在访达中打开这个文件夹")
	}
	withC(strings.Join(items, "\n"), func(c *C.char) { C.ui_items(pid, c, C.int(sel)) })
	pw, ph := fit(pid)
	pw = min(max(pw, 220), 380)
	px := x0 + 56
	setFrame(pid, px, y+(h-ph)/2, pw, ph)
	hasCustom := a.outDir != ""
	a.actions[pid] = func(i int) {
		switch {
		case i == 0:
			a.outMode = "source"
		case i == 1 && hasCustom:
			a.outMode = "custom"
		default:
			// 分隔线之后：「选择文件夹…」「在访达中打开」
			n := i - 2
			if hasCustom {
				n = i - 3
			}
			if n == 0 {
				a.chooseOutDir()
			} else if n == 1 {
				openPath(a.outDir)
			}
		}
		a.saveSettings()
		a.layout()
	}

	// 中间：提示条，或者转换进度
	left := px + pw + 20
	rightEdge := x1 - bw - 16
	if a.toast != "" && rightEdge-left > 140 {
		act := a.toastAction != ""
		actW := 0.0
		if act {
			actW, _ = measure("在访达中显示", C.UI_FONT_BODY, 0)
			actW += 22
		}
		tw, th := measure(a.toast, C.UI_FONT_BODY, 0)
		tw += 8 // 文字框自己的留白
		bwid := min(tw+28+actW, rightEdge-left)
		tb := a.w("toast", C.UI_TOAST, 0)
		C.ui_on(tb, boolInt(a.toastErr))
		tbH := 32.0
		tx := rightEdge - bwid
		ty := y + (h-tbH)/2
		setFrame(tb, tx, ty, bwid, tbH)
		tone := C.int(C.UI_TONE_TEXT)
		if a.toastErr {
			tone = C.UI_TONE_DANGER
		}
		id := a.w("toasttext", C.UI_LABEL, 0)
		setText(id, a.toast)
		C.ui_tone(id, tone)
		withC(a.toast, func(c *C.char) { C.ui_tooltip(id, c) })
		setFrame(id, tx+14, ty+(tbH-th)/2-1, bwid-28-actW, th+2)
		if act {
			target := a.toastAction
			a.button("toastact", 0, C.UI_LINK, "在访达中显示", "", tx+bwid-10, ty, tbH, 0, true, func() { reveal(target) })
		}
	} else if a.sess != nil {
		st := a.statusText()
		tw, th := measure(st, C.UI_FONT_BODY, 0)
		tw += 8
		id := a.w("status", C.UI_LABEL, 0)
		setText(id, st)
		C.ui_tone(id, C.UI_TONE_TEXT2)
		setFrame(id, rightEdge-tw-4, y+(h-th)/2-1, tw+6, th+2)
		sp := a.w("spinner", C.UI_SPINNER, 0)
		setFrame(sp, rightEdge-tw-28, y+(h-16)/2, 16, 16)
	}
}

// shortHome 把用户文件夹写成 ~，太长的路径只留最后两段
func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		p = "~" + p[len(home):]
	}
	if len([]rune(p)) > 44 {
		parts := strings.Split(p, "/")
		if len(parts) > 3 {
			p = parts[0] + "/…/" + parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	return p
}

// ---------------------------------------------------------------- 操作

func (a *macApp) switchPage(i int) {
	if i == a.cur || i < 0 || i >= len(a.pages) {
		return
	}
	a.cur = i
	a.refreshSidebar()
	C.ui_list_reload()
	a.layout()
}

func (a *macApp) chooseTarget(t *catalog.Target) {
	ps := a.page()
	ps.target = t
	t.Defaults(ps.opts)
	C.ui_list_reload()
	a.layout()
}

// addFiles 把文件（或文件夹里的所有文件）加进列表，文件夹在后台展开
func (a *macApp) addFiles(paths []string, switchPage bool) {
	if !hasDir(paths) {
		a.addResolved(paths, false, switchPage)
		return
	}
	a.walking.Add(1)
	a.showToast("正在查找文件夹里的文件…", false, "")
	go func() {
		files, tooMany := collectFiles(paths)
		a.foundMu.Lock()
		a.found = append(a.found, foundFiles{files, tooMany, switchPage})
		a.foundMu.Unlock()
		C.ui_post(postFound)
	}()
}

func (a *macApp) onFilesFound() {
	a.foundMu.Lock()
	batches := a.found
	a.found = nil
	a.foundMu.Unlock()
	for _, b := range batches {
		a.addResolved(b.files, b.tooMany, b.switchPage)
		a.walking.Add(-1)
	}
}

func (a *macApp) addResolved(files []string, tooMany, switchPage bool) {
	msg, isErr, _ := a.core.addResolved(files, tooMany, switchPage)
	a.refreshSidebar()
	C.ui_list_reload()
	if msg != "" {
		a.showToast(msg, isErr, "")
	} else {
		a.layout()
	}
}

func (a *macApp) addFilesDialog() {
	p := a.page().page
	exts := p.Exts()
	sort.Strings(exts)
	var files []string
	withC("选择要转换的"+p.Kind.Name()+"文件（也可以选文件夹）", func(t *C.char) {
		withC(strings.Join(exts, "\n"), func(e *C.char) { files = goLines(C.ui_open_panel(t, e)) })
	})
	if len(files) > 0 {
		a.addFiles(files, true)
	}
}

func (a *macApp) clearList() {
	if a.pageBusy(a.page()) {
		if alert(1, "清空列表会停止正在进行的转换，确定吗？", "", "", "停止并清空", "取消") != 0 {
			return
		}
	}
	a.clearPage()
	a.refreshSidebar()
	C.ui_list_reload()
	a.layout()
}

func (a *macApp) startOrStop() {
	if a.pageBusy(a.page()) {
		a.stopPage()
		a.layout()
		return
	}
	a.start(nil)
}

func (a *macApp) start(only *item) {
	r := a.core.start(only)
	a.explainDenied()
	switch r.kind {
	case startNeedFiles:
		a.addFilesDialog()
		return
	case startWarn:
		alert(1, r.msg, r.detail, "")
		return
	case startError:
		alert(2, r.msg, r.detail, "")
		return
	case startToast:
		a.showToast(r.msg, false, "")
		return
	}
	a.toast = ""
	C.ui_keep_awake(1)
	C.ui_timer(timerBusy, 0.15)
	a.saveSettings()
	C.ui_list_reload()
	a.layout()
}

// explainDenied macOS 没让访问某个文件夹（「隐私与安全性」里没打开）时，告诉用户怎么打开
func (a *macApp) explainDenied() {
	d := takeDenied()
	if d == "" || a.explained[d] {
		return
	}
	a.explained[d] = true
	name := folderName(d)
	if alert(1, "macOS 没有允许「万能格式转换」访问"+name,
		"转换结果会改存到「文稿」里的「万能格式转换」文件夹。\n\n想把结果放在原文件旁边：打开「系统设置」→「隐私与安全性」→「文件与文件夹」，在「万能格式转换」下面打开"+name+"，然后重新点「开始转换」。",
		"", "打开系统设置", "好") == 0 {
		openURL(privacyURL)
	}
}

// folderName 是文件夹在访达里的名字：下载、文稿、桌面……
func folderName(dir string) string {
	home, _ := os.UserHomeDir()
	for sub, name := range map[string]string{"Downloads": "「下载」文件夹", "Documents": "「文稿」文件夹", "Desktop": "「桌面」"} {
		root := filepath.Join(home, sub)
		if dir == root || strings.HasPrefix(dir, root+"/") {
			return name
		}
	}
	if strings.HasPrefix(dir, "/Volumes/") {
		return "外接磁盘或网络磁盘上的文件夹"
	}
	return "这个文件夹（" + dir + "）"
}

func (a *macApp) onTaskUpdate() {
	a.dirty.Store(false)
	C.ui_list_refresh()
	if a.pageBusy(a.page()) != a.btnBusy {
		a.layout()
	}
	if a.sess == nil {
		return
	}
	if a.runner.Busy() > 0 {
		a.updateProgress()
		return
	}
	// 这一批全部结束
	C.ui_timer_stop(timerBusy)
	C.ui_keep_awake(0)
	C.ui_dock(-1, nil)
	withC(appName, func(c *C.char) { C.ui_set_title(c) })
	msg, isErr, action := a.finishSession()
	C.ui_list_refresh()
	a.showToast(msg, isErr, action)
	C.ui_attention()
}

// updateProgress 底栏、程序坞图标上的总进度
func (a *macApp) updateProgress() {
	if a.sess == nil {
		return
	}
	done, total := a.sessionCount()
	left := ""
	if total-done > 0 {
		left = itoa(total - done)
	}
	withC(left, func(c *C.char) { C.ui_dock(C.double(a.overallProgress()), c) })
	st := a.statusText()
	withC(st+" · "+appName, func(c *C.char) { C.ui_set_title(c) })
	if id, ok := a.views["status"]; ok {
		setText(id, st)
	} else if a.toast == "" {
		a.layout()
	}
}

func (a *macApp) showToast(msg string, isErr bool, action string) {
	a.toast, a.toastErr, a.toastAction = msg, isErr, action
	d := 4.5
	if action != "" {
		d = 12
	}
	C.ui_timer(timerToast, C.double(d))
	a.layout()
}

func (a *macApp) chooseOutDir() {
	var d []string
	withC("选择保存转换结果的文件夹", func(t *C.char) {
		withC(a.outDir, func(s *C.char) { d = goLines(C.ui_choose_folder(t, s)) })
	})
	if len(d) == 1 {
		a.outMode, a.outDir = "custom", d[0]
	}
}

// openItem 双击：转换完成的在访达里显示结果，失败的显示原因，其余打开原文件
func (a *macApp) openItem(it *item) {
	if it.task != nil {
		info := a.runner.Snapshot(it.task)
		if info.State == conv.Done && len(info.Outputs) > 0 {
			reveal(info.Outputs[0])
			return
		}
		if info.State == conv.Failed {
			a.showError(it, info.Err)
			return
		}
	}
	openPath(it.path)
}

func (a *macApp) showError(it *item, err error) {
	detail := errDetail(err)
	msg := errText(err)
	if isDenied(msg + detail) {
		if alert(2, filepath.Base(it.path)+" 没有转换成功", msg+"\n\nmacOS 没有允许「万能格式转换」访问这个文件夹：打开「系统设置」→「隐私与安全性」→「文件与文件夹」，允许「万能格式转换」访问它；或者在「保存到」里换一个文件夹。",
			detail, "打开系统设置", "知道了") == 0 {
			openURL(privacyURL)
		}
		return
	}
	alert(2, filepath.Base(it.path)+" 没有转换成功", msg, detail, "知道了")
}

// isDenied 判断错误是不是 macOS 的「文件与文件夹」权限没给
func isDenied(s string) bool {
	return strings.Contains(strings.ToLower(s), "operation not permitted")
}

func (a *macApp) selection() []int {
	buf := make([]C.int, 4096)
	n := int(C.ui_list_selection(&buf[0], C.int(len(buf))))
	out := make([]int, 0, n)
	items := a.page().items
	for _, v := range buf[:n] {
		if int(v) < len(items) {
			out = append(out, int(v))
		}
	}
	return out
}

func (a *macApp) moveSelected(d int) {
	sel := a.selection()
	if len(sel) != 1 {
		return
	}
	i, j := sel[0], sel[0]+d
	if !a.moveItem(i, j) {
		return
	}
	C.ui_list_reload()
	C.ui_list_select(C.int(j))
}

func (a *macApp) removeSelected() {
	sel := a.selection()
	if len(sel) == 0 {
		return
	}
	if a.removeItems(sel) {
		a.showToast("已停止合并，调整好列表后再点「开始转换」", false, "")
	}
	a.refreshSidebar()
	C.ui_list_reload()
	C.ui_list_select(C.int(min(sel[0], len(a.page().items)-1)))
	a.layout()
}

func (a *macApp) showAbout() {
	if alert(3, appName, aboutText(), "", "打开项目主页", "好") == 0 {
		openURL(repoURL)
	}
}

// revealSelected 在访达里显示选中的（或第一个）已经转换好的结果
func (a *macApp) revealSelected() {
	items := a.page().items
	try := func(it *item) bool {
		if it.task == nil {
			return false
		}
		if info := a.runner.Snapshot(it.task); info.State == conv.Done && len(info.Outputs) > 0 {
			reveal(info.Outputs[0])
			return true
		}
		return false
	}
	for _, i := range a.selection() {
		if try(items[i]) {
			return
		}
	}
	for _, it := range items {
		if try(it) {
			return
		}
	}
}

func (a *macApp) hasDone() bool {
	for _, it := range a.page().items {
		if it.task != nil && a.runner.Snapshot(it.task).State == conv.Done {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- Objective-C 调回来的函数

//export goLaunched
func goLaunched() {
	a := mac
	if p, _ := os.Executable(); strings.Contains(p, "/AppTranslocation/") {
		a.showToast("建议把「万能格式转换」拖到「应用程序」文件夹里再打开，访达的「打开方式」里才找得到它", false, "")
	}
	if len(a.pending) > 0 {
		files := a.pending
		a.pending = nil
		a.addFiles(files, true)
	}
	if shotFile != "" {
		// 用定时器开始（不能放在 ui_post 里：主队列的任务里转 runloop，别的 ui_post 就送不进来了）
		C.ui_timer(timerShot, 0.6)
	}
}

//export goAction
func goAction(id C.int, value C.int) {
	if f := mac.actions[id]; f != nil {
		f(int(value))
	}
}

//export goText
func goText(id C.int, s *C.char) {
	a := mac
	if k, ok := a.edits[id]; ok {
		a.page().opts[k] = strings.TrimSpace(C.GoString(s))
	}
}

//export goNav
func goNav(row C.int) { mac.switchPage(int(row)) }

//export goListCount
func goListCount() C.int { return C.int(len(mac.page().items)) }

//export goListRow
func goListRow(row C.int, r *C.UIRow) {
	a := mac
	items := a.page().items
	if int(row) < 0 || int(row) >= len(items) {
		return
	}
	it := items[row]
	state, st, frac, running := a.rowState(it)
	name, result := a.rowTones(it)
	r.name = C.CString(filepath.Base(it.path))
	r.path = C.CString(it.path)
	r.size = C.CString(humanSize(it.size))
	r.state = C.CString(state)
	r.result = C.CString(a.rowResult(it))
	r.nameTone, r.stateTone, r.resultTone = C.int(name), C.int(st), C.int(result)
	r.frac = C.double(frac)
	r.running = boolInt(running)
}

//export goListDouble
func goListDouble(row C.int) {
	items := mac.page().items
	if int(row) >= 0 && int(row) < len(items) {
		mac.openItem(items[row])
	}
}

//export goListMenu
func goListMenu(row C.int) *C.char {
	a := mac
	sel := a.selection()
	items := a.page().items
	if len(sel) == 0 || sel[0] >= len(items) {
		return C.CString("")
	}
	it := items[sel[0]]
	var info conv.TaskInfo
	if it.task != nil {
		info = a.runner.Snapshot(it.task)
	}
	var m []string
	add := func(id int, label string, enabled bool, key string) {
		m = append(m, strconv.Itoa(id)+"\t"+label+"\t"+map[bool]string{true: "1", false: "0"}[enabled]+"\t"+key)
	}
	single := len(sel) == 1
	if single && info.State == conv.Done && len(info.Outputs) > 0 {
		add(cmOpenOut, "打开转换结果", true, "")
		add(cmRevealOut, "在访达中显示结果", true, "")
		m = append(m, "-")
	}
	if single && info.State == conv.Failed {
		add(cmError, "查看失败原因", true, "")
		add(cmCopyErr, "拷贝失败原因", true, "")
		m = append(m, "-")
	}
	add(cmOpen, "打开原文件", single, "")
	add(cmReveal, "在访达中显示原文件", single, "")
	m = append(m, "-")
	add(cmUp, "上移", single && sel[0] > 0, "up")
	add(cmDown, "下移", single && sel[0] < len(items)-1, "down")
	m = append(m, "-")
	if info.State == conv.Running || info.State == conv.Waiting {
		add(cmStop, "停止", true, "")
	}
	if single && (info.State == conv.Done || info.State == conv.Failed || info.State == conv.Cancelled) {
		add(cmRedo, "重新转换", true, "")
	}
	add(cmRemove, "从列表中移除", true, "del")
	return C.CString(strings.Join(m, "\n"))
}

//export goListCommand
func goListCommand(cmd C.int) {
	a := mac
	sel := a.selection()
	items := a.page().items
	if len(sel) == 0 {
		return
	}
	it := items[sel[0]]
	var info conv.TaskInfo
	if it.task != nil {
		info = a.runner.Snapshot(it.task)
	}
	switch int(cmd) {
	case cmOpen:
		openPath(it.path)
	case cmReveal:
		reveal(it.path)
	case cmOpenOut:
		if len(info.Outputs) > 0 {
			openPath(info.Outputs[0])
		}
	case cmRevealOut:
		if len(info.Outputs) > 0 {
			reveal(info.Outputs[0])
		}
	case cmError:
		a.showError(it, info.Err)
	case cmCopyErr:
		if info.Err != nil {
			withC(filepath.Base(it.path)+"："+info.Err.Error(), func(c *C.char) { C.ui_copy_text(c) })
		}
	case cmUp:
		a.moveSelected(-1)
	case cmDown:
		a.moveSelected(1)
	case cmStop:
		for _, i := range sel {
			if t := items[i].task; t != nil {
				a.runner.Cancel(t)
			}
		}
	case cmRedo:
		a.start(a.prepareRedo(it)) // 合并出来的文件整批重新合并
	case cmRemove:
		a.removeSelected()
	}
}

//export goListKey
func goListKey(key C.int) {
	switch key {
	case 1:
		mac.removeSelected()
	case 2:
		mac.moveSelected(-1)
	case 3:
		mac.moveSelected(1)
	}
}

//export goDrop
func goDrop(paths *C.char) {
	if files := strings.Split(C.GoString(paths), "\n"); len(files) > 0 && files[0] != "" {
		mac.addFiles(files, true)
	}
}

//export goOpenFiles
func goOpenFiles(paths *C.char) {
	files := strings.Split(C.GoString(paths), "\n")
	if len(files) == 0 || files[0] == "" {
		return
	}
	if !mac.ready {
		mac.pending = append(mac.pending, files...)
		return
	}
	mac.addFiles(files, true)
}

//export goPaste
func goPaste() {
	if files := goLines(C.ui_paste_files()); len(files) > 0 {
		mac.addFiles(files, true)
	}
}

//export goResize
func goResize() { mac.layout() }

//export goTimer
func goTimer(which C.int) {
	a := mac
	switch which {
	case timerBusy:
		C.ui_list_refresh()
		a.updateProgress()
	case timerToast:
		C.ui_timer_stop(timerToast)
		a.toast, a.toastAction = "", ""
		a.layout()
	case timerShot:
		C.ui_timer_stop(timerShot)
		a.runShot()
	}
}

//export goPosted
func goPosted(what C.int) {
	a := mac
	switch what {
	case postUpdate:
		a.onTaskUpdate()
	case postFound:
		a.onFilesFound()
	case postLayout:
		a.layout()
	}
}

//export goMenu
func goMenu(cmd C.int) {
	a := mac
	switch c := int(cmd); {
	case c == menuAbout:
		a.showAbout()
	case c == menuAdd:
		a.addFilesDialog()
	case c == menuStart:
		if !a.pageBusy(a.page()) {
			a.start(nil)
		}
	case c == menuStop:
		a.stopPage()
		a.layout()
	case c == menuRemove:
		a.removeSelected()
	case c == menuClear:
		a.clearList()
	case c == menuReveal:
		a.revealSelected()
	case c == menuHome:
		openURL(repoURL)
	case c == menuLibreOffice:
		openURL(office.LibreOfficeURL)
	case c >= menuPage && c < menuPage+len(a.pages):
		a.switchPage(c - menuPage)
	}
}

//export goMenuEnabled
func goMenuEnabled(cmd C.int) C.int {
	a := mac
	ps := a.page()
	switch int(cmd) {
	case menuStart:
		return boolInt(!a.pageBusy(ps))
	case menuStop:
		return boolInt(a.pageBusy(ps))
	case menuRemove:
		return boolInt(len(a.selection()) > 0)
	case menuClear:
		return boolInt(len(ps.items) > 0)
	case menuReveal:
		return boolInt(a.hasDone())
	}
	return 1
}

//export goShouldQuit
func goShouldQuit() C.int {
	a := mac
	if a.quitOK {
		return 1
	}
	if a.runner.Busy() > 0 {
		if alert(1, "还有文件正在转换，确定要退出吗？", "退出后正在转换的文件会停下，转了一半的结果会被删掉。", "", "退出", "取消") != 0 {
			return 0
		}
		a.runner.CancelAll()
		// 等正在运行的任务收尾（删掉半成品），最多 3 秒
		for i := 0; i < 60 && a.runner.Busy() > 0; i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	a.quitOK = true
	return 1
}

//export goWillQuit
func goWillQuit() {
	C.ui_keep_awake(0)
	mac.saveSettings()
	catalog.Shutdown()
}

//export goActivated
func goActivated() {
	// 用户可能刚装好 LibreOffice：没找到过的话在后台再找一次
	if catalog.OfficeInfo() == "" {
		go func() {
			office.Refresh()
			if office.Detect().LibreOffice != "" {
				C.ui_post(postLayout)
			}
		}()
	}
}

// ---------------------------------------------------------------- 其他

func forwardToRunning([]string) bool { return false } // macOS 上同一个程序本来就只开一个
func attachConsole()                 {}
