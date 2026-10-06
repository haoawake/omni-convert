//go:build darwin

package main

// 截图模式（开发和 CI 用）：-shot 文件.png 打开窗口后按 -steps 依次操作，再把窗口自己画成 PNG 并退出。
// 和 Windows 版的步骤写法一样，比如：-shot a.png -steps "page:1,target:vid:mp4,set:res=custom,add:/a.mp4|/b.mov,start,waitdone"
// 环境变量 OMNI_SHOT_APPEARANCE=dark 时用深色外观。

/*
#include "ui_darwin.h"
*/
import "C"

import (
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/catalog"
)

var (
	shotFile  string
	shotSteps string
)

const timerShot = 9

func pump(sec float64) { C.ui_pump(C.double(sec)) }

func (a *macApp) runShot() {
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
			a.layout()
		case "add":
			a.addFiles(strings.Split(arg, "|"), true)
			for i := 0; i < 600 && a.walking.Load() > 0; i++ {
				pump(0.02) // 等后台把文件夹展开完
			}
		case "start":
			a.start(nil)
		case "wait":
			ms, _ := strconv.Atoi(arg)
			pump(float64(ms) / 1000)
		case "waitdone":
			for i := 0; i < 1200 && a.runner.Busy() > 0; i++ {
				pump(0.1)
			}
			pump(0.3)
		case "select":
			n, _ := strconv.Atoi(arg)
			C.ui_list_select(C.int(n))
		case "size":
			w, h, _ := strings.Cut(arg, "x")
			wf, _ := strconv.ParseFloat(w, 64)
			hf, _ := strconv.ParseFloat(h, 64)
			C.ui_window_size(C.double(wf), C.double(hf))
			pump(0.1)
		}
	}
	a.layout()
	pump(0.4)
	withC(shotFile, func(c *C.char) { C.ui_save_png(c) })
	a.runner.CancelAll()
	a.quitOK = true
	C.ui_quit()
}
