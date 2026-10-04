//go:build windows

package main

// 画图的小工具：GDI 画矩形和文字（快、文字清晰），GDI+ 画需要抗锯齿的圆角和圆点。

import (
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// rgb 是 0xRRGGBB 形式的颜色
type rgb uint32

func (c rgb) colorref() uintptr {
	return uintptr((c>>16)&0xFF | (c & 0xFF00) | (c&0xFF)<<16)
}

func (c rgb) argb(alpha uint8) uintptr { return uintptr(uint32(alpha)<<24 | uint32(c)) }

func (c rgb) r() float64 { return float64(c >> 16 & 0xFF) }
func (c rgb) g() float64 { return float64(c >> 8 & 0xFF) }
func (c rgb) b() float64 { return float64(c & 0xFF) }

// mix 按 t（0~1）把 c 往 d 靠
func (c rgb) mix(d rgb, t float64) rgb {
	m := func(a, b float64) uint32 { return uint32(math.Round(a + (b-a)*t)) }
	return rgb(m(c.r(), d.r())<<16 | m(c.g(), d.g())<<8 | m(c.b(), d.b()))
}

func hsl(h, s, l float64) rgb {
	h = math.Mod(h, 360) / 360
	f := func(t float64) uint32 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		var q float64
		if l < 0.5 {
			q = l * (1 + s)
		} else {
			q = l + s - l*s
		}
		p := 2*l - q
		var v float64
		switch {
		case t < 1.0/6:
			v = p + (q-p)*6*t
		case t < 0.5:
			v = q
		case t < 2.0/3:
			v = p + (q-p)*(2.0/3-t)*6
		default:
			v = p
		}
		return uint32(math.Round(math.Max(0, math.Min(1, v)) * 255))
	}
	return rgb(f(h+1.0/3)<<16 | f(h)<<8 | f(h-1.0/3))
}

// ---------------------------------------------------------------- GDI

func fill(hdc uintptr, r rect, c rgb) {
	if r.W() <= 0 || r.H() <= 0 {
		return
	}
	pSetDCBrushColor.Call(hdc, c.colorref())
	brush, _, _ := pGetStockObject.Call(18) // DC_BRUSH
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
}

// frame 画 1 像素（或 n 像素）的边框
func frame(hdc uintptr, r rect, n int32, c rgb) {
	fill(hdc, rect{r.Left, r.Top, r.Right, r.Top + n}, c)
	fill(hdc, rect{r.Left, r.Bottom - n, r.Right, r.Bottom}, c)
	fill(hdc, rect{r.Left, r.Top + n, r.Left + n, r.Bottom - n}, c)
	fill(hdc, rect{r.Right - n, r.Top + n, r.Right, r.Bottom - n}, c)
}

// gradientV 从上到下渐变
func gradientV(hdc uintptr, r rect, top, bottom rgb) {
	if r.W() <= 0 || r.H() <= 0 {
		return
	}
	v := [2]triVertex{
		{X: r.Left, Y: r.Top, Red: uint16(top.r()) << 8, Green: uint16(top.g()) << 8, Blue: uint16(top.b()) << 8},
		{X: r.Right, Y: r.Bottom, Red: uint16(bottom.r()) << 8, Green: uint16(bottom.g()) << 8, Blue: uint16(bottom.b()) << 8},
	}
	idx := [2]uint32{0, 1}
	pGradientFill.Call(hdc, uintptr(unsafe.Pointer(&v[0])), 2, uintptr(unsafe.Pointer(&idx[0])), 1, 1) // GRADIENT_FILL_RECT_V
}

const (
	dtLeft         = 0x0
	dtCenter       = 0x1
	dtRight        = 0x2
	dtVCenter      = 0x4
	dtBottom       = 0x8
	dtWordBreak    = 0x10
	dtSingleLine   = 0x20
	dtNoClip       = 0x100
	dtCalcRect     = 0x400
	dtNoPrefix     = 0x800
	dtEndEllipsis  = 0x8000
	dtPathEllipsis = 0x4000
)

func text(hdc, font uintptr, s string, r rect, c rgb, flags uint32) {
	if s == "" || r.W() <= 0 {
		return
	}
	p, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, c.colorref())
	pSetBkMode.Call(hdc, 1) // TRANSPARENT
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), uintptr(flags|dtNoPrefix))
}

// textLine 画一行文字，放不下时末尾用省略号
func textLine(hdc, font uintptr, s string, r rect, c rgb, align uint32) {
	text(hdc, font, s, r, c, align|dtSingleLine|dtVCenter|dtEndEllipsis)
}

func textWidth(hdc, font uintptr, s string) int32 {
	p, err := windows.UTF16FromString(s)
	if err != nil || len(p) <= 1 {
		return 0
	}
	pSelectObject.Call(hdc, font)
	var sz point
	pGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&sz)))
	return sz.X
}

// textHeight 量多行文字在 width 宽度内要占多高
func textHeight(hdc, font uintptr, s string, width int32) int32 {
	p, err := windows.UTF16FromString(s)
	if err != nil || len(p) <= 1 {
		return 0
	}
	pSelectObject.Call(hdc, font)
	r := rect{0, 0, width, 0}
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)-1), uintptr(unsafe.Pointer(&r)), dtWordBreak|dtCalcRect|dtNoPrefix)
	return r.H()
}

func newFont(px int32, weight int, face string) uintptr {
	f, _, _ := pCreateFontW.Call(uintptr(-px), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1 /* DEFAULT_CHARSET */, 0, 0, 5 /* CLEARTYPE_QUALITY */, 0, uintptr(unsafe.Pointer(u16(face))))
	return f
}

// fontExists 看系统里有没有这个字体（没有的话 GDI 会悄悄换成别的字体）
func fontExists(face string) bool {
	dc, _, _ := pCreateCompatibleDC.Call(0)
	defer pDeleteDC.Call(dc)
	f := newFont(16, 400, face)
	defer pDeleteObject.Call(f)
	pSelectObject.Call(dc, f)
	buf := make([]uint16, 64)
	pGetTextFaceW.Call(dc, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	return windows.UTF16ToString(buf) == face
}

func clip(hdc uintptr, r rect) {
	pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
}

func saveDC(hdc uintptr) uintptr {
	n, _, _ := pSaveDC.Call(hdc)
	return n
}

func restoreDC(hdc, n uintptr) { pRestoreDC.Call(hdc, n) }

// buffer 是一块内存画布，先画到这里再一次性贴到窗口上，不闪
type buffer struct {
	dc, bmp, old uintptr
	w, h         int32
}

func (b *buffer) ensure(ref uintptr, w, h int32) {
	if b.dc != 0 && b.w == w && b.h == h {
		return
	}
	b.free()
	w, h = max(w, 1), max(h, 1)
	b.dc, _, _ = pCreateCompatibleDC.Call(ref)
	b.bmp, _, _ = pCreateCompatibleBitmap.Call(ref, uintptr(w), uintptr(h))
	b.old, _, _ = pSelectObject.Call(b.dc, b.bmp)
	b.w, b.h = w, h
}

func (b *buffer) free() {
	if b.dc == 0 {
		return
	}
	pSelectObject.Call(b.dc, b.old)
	pDeleteObject.Call(b.bmp)
	pDeleteDC.Call(b.dc)
	*b = buffer{}
}

func blit(dst uintptr, x, y, w, h int32, src uintptr, sx, sy int32) {
	pBitBlt.Call(dst, uintptr(x), uintptr(y), uintptr(w), uintptr(h), src, uintptr(sx), uintptr(sy), srcCopy)
}

// ---------------------------------------------------------------- GDI+

var gdiplusToken uintptr

func startGdiplus() {
	input := struct {
		Version                  uint32
		DebugEventCallback       uintptr
		SuppressBackgroundThread int32
		SuppressExternalCodecs   int32
	}{Version: 1}
	pGdiplusStartup.Call(uintptr(unsafe.Pointer(&gdiplusToken)), uintptr(unsafe.Pointer(&input)), 0)
}

// gp 包装一个 GDI+ 画布。注意 GDI+ 的平面接口里凡是浮点参数都不能用 syscall 直接传，
// 这里只用全整数的接口。
type gp struct{ g uintptr }

func newGP(hdc uintptr) gp {
	var g uintptr
	pGdipCreateFromHDC.Call(hdc, uintptr(unsafe.Pointer(&g)))
	pGdipSetSmoothingMode.Call(g, 4)   // SmoothingModeAntiAlias
	pGdipSetPixelOffsetMode.Call(g, 4) // PixelOffsetModeHalf：整数坐标正好落在像素边上
	return gp{g}
}

func (p gp) close() { pGdipDeleteGraphics.Call(p.g) }

func (p gp) brush(c rgb, alpha uint8) uintptr {
	var b uintptr
	pGdipCreateSolidFill.Call(c.argb(alpha), uintptr(unsafe.Pointer(&b)))
	return b
}

// roundRect 填一个圆角矩形。圆角用三次贝塞尔曲线拼（控制点取 0.5523·r）。
func (p gp) roundRect(r rect, radius int32, c rgb, alpha uint8) {
	if r.W() <= 0 || r.H() <= 0 {
		return
	}
	radius = min(radius, r.W()/2, r.H()/2)
	if radius <= 0 {
		b := p.brush(c, alpha)
		pGdipFillRectangleI.Call(p.g, b, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()))
		pGdipDeleteBrush.Call(b)
		return
	}
	L, T, R, B, rr := r.Left, r.Top, r.Right, r.Bottom, radius
	k := int32(math.Round(float64(rr) * 0.4477)) // rr - 0.5523·rr
	line := func(x1, y1, x2, y2 int32) []point {
		return []point{{x1, y1}, {x2, y2}, {x2, y2}}
	}
	pts := []point{{L + rr, T}}
	pts = append(pts, line(L+rr, T, R-rr, T)...)
	pts = append(pts, point{R - k, T}, point{R, T + k}, point{R, T + rr})
	pts = append(pts, line(R, T+rr, R, B-rr)...)
	pts = append(pts, point{R, B - k}, point{R - k, B}, point{R - rr, B})
	pts = append(pts, line(R-rr, B, L+rr, B)...)
	pts = append(pts, point{L + k, B}, point{L, B - k}, point{L, B - rr})
	pts = append(pts, line(L, B-rr, L, T+rr)...)
	pts = append(pts, point{L, T + k}, point{L + k, T}, point{L + rr, T})

	var path uintptr
	pGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&path)))
	pGdipAddPathBeziersI.Call(path, uintptr(unsafe.Pointer(&pts[0])), uintptr(len(pts)))
	pGdipClosePathFigure.Call(path)
	b := p.brush(c, alpha)
	pGdipFillPath.Call(p.g, b, path)
	pGdipDeleteBrush.Call(b)
	pGdipDeletePath.Call(path)
}

// roundBox 画一个带 1 像素边框的圆角矩形
func (p gp) roundBox(r rect, radius int32, bg, border rgb) {
	p.roundRect(r, radius, border, 255)
	p.roundRect(rect{r.Left + 1, r.Top + 1, r.Right - 1, r.Bottom - 1}, radius-1, bg, 255)
}

func (p gp) ellipse(r rect, c rgb, alpha uint8) {
	b := p.brush(c, alpha)
	pGdipFillEllipseI.Call(p.g, b, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()))
	pGdipDeleteBrush.Call(b)
}

func (p gp) rectFill(r rect, c rgb, alpha uint8) {
	b := p.brush(c, alpha)
	pGdipFillRectangleI.Call(p.g, b, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()))
	pGdipDeleteBrush.Call(b)
}

// spinner 画一圈 8 个小圆点，frame 决定哪个最亮，转起来就是「正在进行」
func (p gp) spinner(cx, cy, radius, dot int32, frame int, c rgb) {
	for i := 0; i < 8; i++ {
		a := float64(i) * math.Pi / 4
		x := cx + int32(math.Round(math.Sin(a)*float64(radius)))
		y := cy - int32(math.Round(math.Cos(a)*float64(radius)))
		age := (frame - i + 8*100) % 8
		alpha := uint8(255 - age*28)
		p.ellipse(rect{x - dot/2, y - dot/2, x - dot/2 + dot, y - dot/2 + dot}, c, alpha)
	}
}
