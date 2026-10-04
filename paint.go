//go:build windows

package main

// 画界面：先画背景和卡片，再按 a.scene 一个个画控件。

import (
	"unsafe"
)

// 配色：洁白、浅灰，强调色是蓝色（和文件清理助手一致）
const (
	cBg         rgb = 0xF3F5F8
	cBar        rgb = 0xFBFCFD
	cNav        rgb = 0xF7F8FA
	cLine       rgb = 0xE2E6ED
	cCard       rgb = 0xFFFFFF
	cText       rgb = 0x15171C
	cText2      rgb = 0x5C616D
	cText3      rgb = 0x979CA8
	cAccent     rgb = 0x0A6CFF
	cAccentDark rgb = 0x0B5BD3
	cAccentSoft rgb = 0xE6F0FF
	cBtn        rgb = 0xFFFFFF
	cBtnHover   rgb = 0xF2F5F9
	cBtnPress   rgb = 0xE6EAF0
	cBtnBorder  rgb = 0xD8DDE6
	cOK         rgb = 0x0E9F57
	cWarn       rgb = 0xB86200
	cWarnSoft   rgb = 0xFFF4E5
	cDanger     rgb = 0xE5484D
	cSoft       rgb = 0xEEF1F5
)

func roundRect(dc uintptr, r rect, radius int32, c rgb) {
	g := newGP(dc)
	g.roundRect(r, radius, c, 255)
	g.close()
}

func roundBox(dc uintptr, r rect, radius int32, bg, border rgb) {
	g := newGP(dc)
	g.roundBox(r, radius, bg, border)
	g.close()
}

func (a *app) paint() {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(a.hwnd, uintptr(unsafe.Pointer(&ps)))
	cr := clientRect(a.hwnd)
	a.buf.ensure(hdc, cr.W(), cr.H())
	dc := a.buf.dc

	fill(dc, cr, cBg)
	a.paintChrome(dc)
	for i := range a.scene {
		a.paintWidget(dc, &a.scene[i])
	}
	p := ps.Paint
	blit(hdc, p.Left, p.Top, p.W(), p.H(), dc, p.Left, p.Top)
	pEndPaint.Call(a.hwnd, uintptr(unsafe.Pointer(&ps)))
}

// paintChrome 画顶栏、左侧导航、两张卡片和底栏的底色
func (a *app) paintChrome(dc uintptr) {
	// 顶栏
	r := a.rTop
	fill(dc, r, cBar)
	fill(dc, rect{r.Left, r.Bottom - 1, r.Right, r.Bottom}, cLine)
	if a.appIcon == 0 {
		a.appIcon, _, _ = pLoadImageW.Call(moduleHandle(), 1, 1, uintptr(a.scale(24)), uintptr(a.scale(24)), 0)
	}
	if a.appIcon != 0 {
		user32.NewProc("DrawIconEx").Call(dc, uintptr(a.scale(20)), uintptr(r.Top+(r.H()-a.scale(24))/2), a.appIcon, uintptr(a.scale(24)), uintptr(a.scale(24)), 0, 0, 3)
	} else {
		ir := rect{a.scale(20), r.Top + (r.H()-a.scale(24))/2, a.scale(44), r.Top + (r.H()-a.scale(24))/2 + a.scale(24)}
		roundRect(dc, ir, a.scale(6), cAccent)
	}

	// 左侧导航
	n := a.rNav
	fill(dc, n, cNav)
	fill(dc, rect{n.Right - 1, n.Top, n.Right, n.Bottom}, cLine)

	// 两张卡片
	for _, c := range []rect{a.rOpts, a.rFiles} {
		if c.W() <= 0 {
			continue
		}
		roundBox(dc, c, a.scale(12), cCard, cLine)
		fill(dc, rect{c.Left + 1, c.Top + a.scale(46), c.Right - 1, c.Top + a.scale(46) + 1}, cLine)
	}

	// 选项太多放不下时，在右边画一条细细的滚动条
	if a.optHeight > a.rOptsBody.H() && a.rOptsBody.H() > 0 {
		body := a.rOptsBody
		track := body.H() - a.scale(16)
		thumb := max(track*body.H()/a.optHeight, a.scale(30))
		top := body.Top + a.scale(8) + (track-thumb)*a.optScroll/max(a.optHeight-body.H(), 1)
		roundRect(dc, rect{body.Right - a.scale(7), top, body.Right - a.scale(3), top + thumb}, a.scale(2), 0xC9CFD8)
	}

	// 底栏
	b := a.rAction
	fill(dc, b, cBar)
	fill(dc, rect{b.Left, b.Top, b.Right, b.Top + 1}, cLine)
}

func (a *app) paintWidget(dc uintptr, w *widget) {
	if w.clip.W() > 0 {
		if w.r.Bottom <= w.clip.Top || w.r.Top >= w.clip.Bottom {
			return
		}
		n := saveDC(dc)
		clip(dc, w.clip)
		defer restoreDC(dc, n)
	}
	hot := a.hotID == w.id && w.click != nil
	pressed := hot && a.pressID == w.id
	switch w.kind {
	case wText:
		switch w.id {
		case "toast":
			a.paintToast(dc, w)
		case "dropzone":
			a.paintDropZone(dc, w.r)
		case "banner":
			bg := cSoft
			if w.on {
				bg = cWarnSoft
			}
			roundRect(dc, w.r, a.scale(8), bg)
			text(dc, a.f.icon, w.glyph, rect{w.r.Left + a.scale(14), w.r.Top + a.scale(8), w.r.Left + a.scale(34), w.r.Top + a.scale(28)}, w.color, dtLeft|dtSingleLine)
			text(dc, w.font, w.text, rect{w.r.Left + a.scale(38), w.r.Top + a.scale(8), w.r.Right - a.scale(10), w.r.Bottom}, w.color, dtWordBreak)
		case "status":
			if a.toast == "" {
				textLine(dc, w.font, a.statusText(), w.r, w.color, w.align)
			}
		case "hint":
			text(dc, a.f.iconSmall, w.glyph, rect{w.r.Left, w.r.Top, w.r.Left + a.scale(18), w.r.Bottom}, w.color, dtLeft|dtSingleLine|dtVCenter)
			textLine(dc, w.font, w.text, rect{w.r.Left + a.scale(20), w.r.Top, w.r.Right, w.r.Bottom}, w.color, dtLeft)
		default:
			if w.wrap {
				text(dc, w.font, w.text, w.r, w.color, dtWordBreak)
			} else {
				textLine(dc, w.font, w.text, w.r, w.color, w.align)
			}
		}

	case wNav:
		fg := cText2
		if w.on {
			roundBox(dc, w.r, a.scale(10), cCard, cLine)
			fill(dc, rect{w.r.Left + a.scale(1), w.r.Top + a.scale(14), w.r.Left + a.scale(4), w.r.Bottom - a.scale(14)}, cAccent)
			fg = cAccent
		} else if hot {
			roundRect(dc, w.r, a.scale(10), cBtnPress.mix(cNav, 0.4))
		}
		x := w.r.Left + a.scale(18)
		textLine(dc, a.f.iconNav, w.glyph, rect{x, w.r.Top, x + a.scale(24), w.r.Bottom}, fg, dtLeft)
		x += a.scale(34)
		font, tc := a.f.nav, cText
		if w.on {
			font, tc = a.f.navBold, cAccent
		}
		textLine(dc, font, w.text, rect{x, w.r.Top, w.r.Right - a.scale(40), w.r.Bottom}, tc, dtLeft)
		if w.badge != "" {
			bw := max(a.measure(a.f.small, w.badge)+a.scale(14), a.scale(24))
			br := rect{w.r.Right - a.scale(12) - bw, w.r.Top + (w.r.H()-a.scale(20))/2, w.r.Right - a.scale(12), w.r.Top + (w.r.H()-a.scale(20))/2 + a.scale(20)}
			bg, bc := cSoft, cText2
			if w.on {
				bg, bc = cAccentSoft, cAccent
			}
			roundRect(dc, br, a.scale(10), bg)
			textLine(dc, a.f.small, w.badge, br, bc, dtCenter)
		}

	case wChip:
		bg, border, fg := cBtn, cBtnBorder, cText
		usable := w.badge != ""
		switch {
		case w.on:
			bg, border, fg = cAccent, cAccent, 0xFFFFFF
		case pressed:
			bg = cBtnPress
		case hot:
			bg, border = cAccentSoft, cAccent.mix(0xFFFFFF, 0.55)
		}
		if !usable && !w.on {
			fg = cText3
		}
		roundBox(dc, w.r, a.scale(8), bg, border)
		textLine(dc, w.font, w.text, w.r, fg, dtCenter)

	case wSelect:
		bg := cBtn
		if pressed {
			bg = cBtnPress
		} else if hot {
			bg = cBtnHover
		}
		roundBox(dc, w.r, a.scale(7), bg, cBtnBorder)
		x := w.r.Left + a.scale(10)
		if w.glyph != "" {
			textLine(dc, a.f.icon, w.glyph, rect{x, w.r.Top, x + a.scale(20), w.r.Bottom}, cText2, dtLeft)
			x += a.scale(26)
		}
		textLine(dc, a.f.ui, w.text, rect{x, w.r.Top, w.r.Right - a.scale(26), w.r.Bottom}, cText, dtLeft)
		textLine(dc, a.f.iconSmall, gChevron, rect{w.r.Right - a.scale(24), w.r.Top, w.r.Right - a.scale(8), w.r.Bottom}, cText2, dtCenter)

	case wSeg:
		a.paintSeg(dc, w, hot, pressed)

	case wCheck:
		box := rect{w.r.Left, w.r.Top + (w.r.H()-a.scale(18))/2, w.r.Left + a.scale(18), w.r.Top + (w.r.H()-a.scale(18))/2 + a.scale(18)}
		if w.on {
			roundRect(dc, box, a.scale(4), cAccent)
			textLine(dc, a.f.iconSmall, gCheck, box, 0xFFFFFF, dtCenter)
		} else {
			border := cBtnBorder.mix(0x000000, 0.08)
			if hot {
				border = cAccent
			}
			roundBox(dc, box, a.scale(4), cBtn, border)
		}
		textLine(dc, a.f.ui, w.text, rect{box.Right + a.scale(8), w.r.Top, w.r.Right + a.scale(40), w.r.Bottom}, cText, dtLeft)

	case wEdit:
		roundBox(dc, w.r, a.scale(7), cBtn, cBtnBorder)

	case wButton, wPrimary, wDanger, wGhost:
		a.paintButton(dc, w, hot, pressed)

	case wLink:
		c := rgb(0x8AB8FF)
		if hot {
			c = 0xFFFFFF
		}
		textLine(dc, a.f.uiBold, w.text, w.r, c, dtCenter)
	}
}

func (a *app) paintButton(dc uintptr, w *widget, hot, pressed bool) {
	bg, border, fg, gc := cBtn, cBtnBorder, cText, cText2
	radius := a.scale(8)
	switch w.kind {
	case wPrimary, wDanger:
		base := cAccent
		if w.kind == wDanger {
			base = cDanger
		}
		bg, border, fg, gc = base, base, 0xFFFFFF, 0xFFFFFF
		if hot {
			bg = base.mix(0xFFFFFF, 0.1)
		}
		if pressed {
			bg = base.mix(0x000000, 0.12)
		}
		border = bg
		radius = a.scale(10)
	case wGhost:
		bg, border = cBar, cBar
		if w.r.Top > a.rTop.Bottom {
			bg, border = cCard, cCard
		}
		if w.on {
			fg, gc = cOK, cOK
		}
		if hot {
			bg = cBtnHover
			border = bg
		}
		if pressed {
			bg = cBtnPress
			border = bg
		}
	default:
		if pressed {
			bg = cBtnPress
		} else if hot {
			bg = cBtnHover
		}
	}
	if !w.enabled {
		fg, gc = cText3, cText3
	}
	roundBox(dc, w.r, radius, bg, border)
	font := w.font
	if font == 0 {
		font = a.f.ui
	}
	tw := a.measure(font, w.text)
	total := tw
	if w.glyph != "" {
		total += a.scale(24)
	}
	x := w.r.Left + (w.r.W()-total)/2
	if w.glyph != "" {
		textLine(dc, a.f.icon, w.glyph, rect{x, w.r.Top, x + a.scale(20), w.r.Bottom}, gc, dtLeft)
		x += a.scale(24)
	}
	textLine(dc, font, w.text, rect{x, w.r.Top, x + tw + 2, w.r.Bottom}, fg, dtLeft)
}

// paintSeg 画分段按钮的一段：整组共用一个圆角外框
func (a *app) paintSeg(dc uintptr, w *widget, hot, pressed bool) {
	radius := a.scale(7)
	r := w.r
	// 先按整段画圆角，再用直角盖住和相邻段相接的那一边
	bg := cBtn
	if w.on {
		bg = cAccentSoft
	} else if pressed {
		bg = cBtnPress
	} else if hot {
		bg = cBtnHover
	}
	border := cBtnBorder
	if w.on {
		border = cAccent.mix(0xFFFFFF, 0.35)
	}
	g := newGP(dc)
	g.roundBox(r, radius, bg, border)
	g.close()
	if w.seg&1 == 0 { // 左边接着前一段
		fill(dc, rect{r.Left, r.Top + 1, r.Left + radius, r.Bottom - 1}, bg)
		fill(dc, rect{r.Left, r.Top, r.Left + radius, r.Top + 1}, border)
		fill(dc, rect{r.Left, r.Bottom - 1, r.Left + radius, r.Bottom}, border)
		fill(dc, rect{r.Left, r.Top, r.Left + 1, r.Bottom}, border.mix(bg, 0.3))
	}
	if w.seg&2 == 0 { // 右边接着后一段
		fill(dc, rect{r.Right - radius, r.Top + 1, r.Right, r.Bottom - 1}, bg)
		fill(dc, rect{r.Right - radius, r.Top, r.Right, r.Top + 1}, border)
		fill(dc, rect{r.Right - radius, r.Bottom - 1, r.Right, r.Bottom}, border)
	}
	fg := cText
	font := a.f.ui
	if w.on {
		fg = cAccentDark
		font = a.f.uiBold
	}
	textLine(dc, font, w.text, r, fg, dtCenter)
}

func (a *app) paintDropZone(dc uintptr, r rect) {
	roundRect(dc, r, a.scale(12), 0xF7FAFF)
	// 虚线框
	dash, gap := a.scale(8), a.scale(6)
	c := cAccent.mix(0xFFFFFF, 0.55)
	for x := r.Left + a.scale(12); x < r.Right-a.scale(12); x += dash + gap {
		e := min(x+dash, r.Right-a.scale(12))
		fill(dc, rect{x, r.Top, e, r.Top + a.scale(1.5)}, c)
		fill(dc, rect{x, r.Bottom - a.scale(1.5), e, r.Bottom}, c)
	}
	for y := r.Top + a.scale(12); y < r.Bottom-a.scale(12); y += dash + gap {
		e := min(y+dash, r.Bottom-a.scale(12))
		fill(dc, rect{r.Left, y, r.Left + a.scale(1.5), e}, c)
		fill(dc, rect{r.Right - a.scale(1.5), y, r.Right, e}, c)
	}
}

func (a *app) paintToast(dc uintptr, w *widget) {
	bg := rgb(0x1F2329)
	if w.on {
		bg = 0xC93A3F
	}
	roundRect(dc, w.r, a.scale(12), bg)
	tr := rect{w.r.Left + a.scale(20), w.r.Top, w.r.Right - a.scale(20), w.r.Bottom}
	if a.toastAction != "" {
		tr.Right -= a.measure(a.f.uiBold, "打开文件夹") + a.scale(28)
	}
	textLine(dc, a.f.ui, w.text, tr, 0xFFFFFF, dtLeft)
}
