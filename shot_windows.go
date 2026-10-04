//go:build windows

package main

// 截图模式（开发时用）：-shot 文件.png 打开窗口后按 -steps 依次操作，再把窗口截图存下来并退出。
// 比如：-shot a.png -steps "page:1,target:vid:mp4,set:res=custom,add:D:\a.mp4,start,wait:3000"

import (
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/haoawake/omni-convert/internal/catalog"
)

var (
	shotFile  string
	shotSteps string
)

const timerShot = 9

func (a *app) scheduleShot() {
	if shotFile != "" {
		pSetTimer.Call(a.hwnd, timerShot, 400, 0)
	}
}

func (a *app) pump(ms int) {
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		var m msg
		for {
			r, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
			if r == 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (a *app) onShotTimer() {
	pKillTimer.Call(a.hwnd, timerShot)
	for _, step := range strings.Split(shotSteps, ",") {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(step), ":")
		switch cmd {
		case "page":
			n, _ := strconv.Atoi(arg)
			a.switchPage(n)
		case "target":
			if t := catalog.ByID(arg); t != nil {
				a.chooseTarget(t)
			}
		case "set":
			k, v, _ := strings.Cut(arg, "=")
			a.page().opts[k] = v
			a.syncEdits()
			a.layout()
		case "add":
			a.addFiles(strings.Split(arg, "|"), true)
		case "start":
			a.start(nil)
		case "wait":
			ms, _ := strconv.Atoi(arg)
			a.pump(ms)
		case "waitdone":
			for i := 0; i < 1200 && a.runner.Busy() > 0; i++ {
				a.pump(100)
			}
			a.pump(300)
		case "hover":
			xy := strings.Split(arg, ":")
			x, _ := strconv.Atoi(xy[0])
			y, _ := strconv.Atoi(xy[1])
			a.onMouseMove(int32(x), int32(y))
		case "select":
			n, _ := strconv.Atoi(arg)
			a.lv.selectOnly(n)
		case "size":
			wh := strings.Split(arg, "x")
			w, _ := strconv.Atoi(wh[0])
			h, _ := strconv.Atoi(wh[1])
			pSetWindowPos.Call(a.hwnd, 0, 0, 0, uintptr(w), uintptr(h), 0x2|0x4)
			a.pump(100)
		}
	}
	a.layout()
	invalidate(a.hwnd, nil)
	user32.NewProc("UpdateWindow").Call(a.hwnd)
	a.pump(150)
	saveWindowShot(a.hwnd, shotFile)
	a.runner.CancelAll()
	pDestroyWindow.Call(a.hwnd)
}

func saveWindowShot(hwnd uintptr, file string) {
	cr := clientRect(hwnd)
	dc, _, _ := pGetDC.Call(hwnd)
	var b buffer
	b.ensure(dc, cr.W(), cr.H())
	pPrintWindow.Call(hwnd, b.dc, 1|2)
	pReleaseDC.Call(hwnd, dc)
	w, h := cr.W(), cr.H()
	bi := struct {
		Size                          uint32
		Width, Height                 int32
		Planes, BitCount              uint16
		Compression, SizeImage        uint32
		XPels, YPels, ClrUsed, ClrImp int32
	}{Width: w, Height: -h, Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	pix := make([]byte, w*h*4)
	pSelectObject.Call(b.dc, b.old)
	pGetDIBits.Call(b.dc, b.bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0)
	pSelectObject.Call(b.dc, b.bmp)
	b.free()
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < len(pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pix[i+2], pix[i+1], pix[i], 255
	}
	f, err := os.Create(file)
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}
