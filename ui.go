//go:build windows

package main

// 主窗口：窗口过程、消息循环、鼠标键盘，以及「添加文件」「开始转换」这些操作。
// 界面除了文件列表和输入框，全部自己画（见 layout.go、paint.go）。

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
)

// 自定义消息
const (
	wmTaskUpdate = wmApp + 1 // 任务状态变了
	wmGPUReady   = wmApp + 2 // 显卡检测完了
)

// 定时器
const (
	timerBusy  = 1 // 有任务在跑：刷新进度、转圈
	timerToast = 2
	timerFlush = 3 // 别的实例转交过来的文件攒一会儿再一起加
)

type fonts struct {
	ui, uiBold, small, smallBold, title, nav, navBold, chip, icon, iconSmall, iconBig, iconNav uintptr
}

// item 是文件列表里的一行
type item struct {
	path    string
	size    int64
	task    *conv.Task // 最近一次转换
	key     string     // 最近一次转换用的目标和选项，没变就不重复转
	outText string     // 转换结果的说明（完成后算一次）
}

// pageState 是左侧每一页各自的状态
type pageState struct {
	page   *catalog.Page
	target *catalog.Target
	opts   conv.Options
	items  []*item
}

// session 是点一次「开始转换」启动的一批任务
type session struct {
	tasks   []*conv.Task
	page    int
	started time.Time
}

type app struct {
	hwnd    uintptr
	dpi     int32
	s       float64
	f       fonts
	icons   string
	appIcon uintptr

	pages  []*pageState
	cur    int
	runner *conv.Runner
	sess   *session
	frame  int

	outMode string // source = 和原文件放在一起；custom = 指定文件夹
	outDir  string

	toast       string
	toastErr    bool
	toastAction string // 非空时 toast 上有个「打开文件夹」，点了打开它
	pending     []string

	scene   []widget
	hotID   string
	pressID string
	mouse   point
	mouseIn bool

	rTop, rNav, rContent, rOpts, rOptsBody, rFiles, rList, rAction rect
	optScroll, optHeight                                           int32

	buf       buffer
	lv        *fileList
	edits     map[string]uintptr // 选项输入框：选项的键 → 窗口句柄
	editProc  uintptr
	editBrush uintptr
	syncing   bool // 程序在改输入框的内容，不要当成用户输入
	menuOn    bool // 右键菜单已经加上
	dirty     atomic.Bool
}

var theApp *app

func runApp(initial []string) {
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if pSetProcessDpiAwarenessContext.Find() == nil {
		pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2
	}
	icc := struct{ Size, ICC uint32 }{8, 0x1 | 0x4000}
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
	startGdiplus()

	a := &app{edits: map[string]uintptr{}, outMode: "source"}
	theApp = a
	a.icons = "Segoe Fluent Icons"
	if !fontExists(a.icons) {
		a.icons = "Segoe MDL2 Assets"
	}
	for _, p := range catalog.Pages {
		a.pages = append(a.pages, &pageState{page: p, target: p.Targets[0], opts: conv.Options{}})
	}
	// 默认目标：图片 JPG、视频 MP4、音频 MP3、文档 PDF、PDF 转 Word
	for i, id := range []string{"img:jpg", "vid:mp4", "aud:mp3", "doc:pdf", "pdf:docx"} {
		if t := catalog.ByID(id); t != nil {
			a.pages[i].target = t
		}
	}
	a.loadSettings()
	for _, ps := range a.pages {
		ps.target.Defaults(ps.opts)
	}
	a.runner = conv.NewRunner(map[string]int{
		"image": 4, "video": 2, "audio": 3, "office": 1, "pdf": 2,
	}, func(*conv.Task) {
		if !a.dirty.Swap(true) {
			postMessage(a.hwnd, wmTaskUpdate, 0, 0)
		}
	})

	inst := moduleHandle()
	cursor, _, _ := pLoadCursorW.Call(0, 32512)
	icon, _, _ := pLoadImageW.Call(inst, 1, 1, 0, 0, 0x40|0x8000)
	cls := wndClassEx{
		Style:     0x8 | 0x1 | 0x2,
		WndProc:   syscall.NewCallback(wndProc),
		Instance:  inst,
		Icon:      icon,
		Cursor:    cursor,
		ClassName: u16(windowClass),
		IconSm:    icon,
	}
	cls.Size = uint32(unsafe.Sizeof(cls))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))

	hwnd, _, _ := pCreateWindowExW.Call(0x10 /* WS_EX_ACCEPTFILES */, uintptr(unsafe.Pointer(u16(windowClass))),
		uintptr(unsafe.Pointer(u16(appName))), wsOverlappedWindow|wsClipChildren,
		0x80000000, 0x80000000, 1200, 800, 0, 0, inst, 0)
	if hwnd == 0 {
		showError("没法创建窗口")
		os.Exit(1)
	}
	a.placeWindow()
	allowDropFromExplorer(hwnd)
	a.menuOn = contextMenuInstalled()
	pShowWindow.Call(hwnd, swShow)
	catalog.DetectGPU(func() { postMessage(hwnd, wmGPUReady, 0, 0) })

	if len(initial) > 0 {
		a.addFiles(initial, true)
	}
	a.layout()
	a.scheduleShot()

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if a.preTranslate(&m) {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	a.saveSettings()
	catalog.Shutdown()
}

func (a *app) page() *pageState { return a.pages[a.cur] }

func (a *app) placeWindow() {
	mon, _, _ := pMonitorFromWindow.Call(a.hwnd, 2)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	wa := mi.Work
	w := min(int32(1320*a.s), wa.W()*90/100)
	h := min(int32(880*a.s), wa.H()*90/100)
	pSetWindowPos.Call(a.hwnd, 0, uintptr(wa.Left+(wa.W()-w)/2), uintptr(wa.Top+(wa.H()-h)/2), uintptr(w), uintptr(h), 0x4)
}

func allowDropFromExplorer(hwnd uintptr) {
	p := user32.NewProc("ChangeWindowMessageFilterEx")
	if p.Find() != nil {
		return
	}
	for _, m := range []uintptr{wmDropFiles, wmCopyData, 0x0049} {
		p.Call(hwnd, m, 1, 0)
	}
}

func (a *app) scale(v float64) int32 { return int32(math.Round(v * a.s)) }

func (a *app) setDPI(dpi int32) {
	if dpi <= 0 {
		dpi = 96
	}
	a.dpi = dpi
	a.s = float64(dpi) / 96
	for _, f := range []uintptr{a.f.ui, a.f.uiBold, a.f.small, a.f.smallBold, a.f.title, a.f.nav, a.f.navBold, a.f.chip, a.f.icon, a.f.iconSmall, a.f.iconBig, a.f.iconNav} {
		if f != 0 {
			pDeleteObject.Call(f)
		}
	}
	const face = "Microsoft YaHei UI"
	a.f = fonts{
		ui:        newFont(a.scale(13), 400, face),
		uiBold:    newFont(a.scale(13), 700, face),
		small:     newFont(a.scale(12), 400, face),
		smallBold: newFont(a.scale(12), 700, face),
		title:     newFont(a.scale(22), 700, face),
		nav:       newFont(a.scale(14), 400, face),
		navBold:   newFont(a.scale(14), 700, face),
		chip:      newFont(a.scale(13), 400, face),
		icon:      newFont(a.scale(16), 400, a.icons),
		iconSmall: newFont(a.scale(12), 400, a.icons),
		iconBig:   newFont(a.scale(40), 400, a.icons),
		iconNav:   newFont(a.scale(18), 400, a.icons),
	}
	if a.lv != nil {
		a.lv.setFont(a.f.ui, a.s)
	}
	for _, e := range a.edits {
		sendMessage(e, wmSetFont, a.f.ui, 1)
	}
	if a.appIcon != 0 {
		a.appIcon = 0 // 换个尺寸重新加载
	}
}

// ---------------------------------------------------------------- 窗口过程

func wndProc(hwnd, m, wp, lp uintptr) uintptr {
	a := theApp
	switch m {
	case wmCreate:
		a.hwnd = hwnd
		mainWindow.Store(hwnd)
		dpi, _, _ := pGetDpiForWindow.Call(hwnd)
		a.setDPI(int32(dpi))
		a.lv = newFileList(hwnd, a.f.ui, a.s)
		return 0

	case wmSize:
		a.layout()
		invalidate(hwnd, nil)
		return 0

	case wmDpiChanged:
		a.setDPI(hi16(wp))
		r := (*rect)(ptr(lp))
		pSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()), 0x4|0x10)
		a.layout()
		invalidate(hwnd, nil)
		return 0

	case wmGetMinMaxInfo:
		if a.s > 0 {
			mmi := (*[5]point)(ptr(lp))
			mmi[3] = point{a.scale(1060), a.scale(680)}
		}
		return 0

	case wmEraseBkgnd:
		return 1

	case wmPaint:
		a.paint()
		return 0

	case wmCtlColorEdit:
		pSetTextColor.Call(wp, cText.colorref())
		pSetBkColor.Call(wp, rgb(0xFFFFFF).colorref())
		if a.editBrush == 0 {
			a.editBrush, _, _ = pCreateSolidBrush.Call(rgb(0xFFFFFF).colorref())
		}
		return a.editBrush

	case wmSetCursor:
		if lo16(lp) == 1 && wp == hwnd {
			id := uintptr(32512)
			if a.hotID != "" {
				id = 32649
			}
			c, _, _ := pLoadCursorW.Call(0, id)
			pSetCursor.Call(c)
			return 1
		}

	case wmMouseMove:
		a.onMouseMove(lo16(lp), hi16(lp))
		return 0

	case wmMouseLeave:
		a.mouseIn = false
		a.setHot("")
		return 0

	case wmLButtonDown:
		pSetFocus.Call(hwnd)
		if a.hotID != "" {
			a.pressID = a.hotID
			pSetCapture.Call(hwnd)
			invalidate(hwnd, nil)
		}
		return 0

	case wmLButtonUp:
		if a.pressID != "" {
			id := a.pressID
			a.pressID = ""
			pReleaseCapture.Call()
			invalidate(hwnd, nil)
			if id == a.hotID {
				a.click(id)
			}
		}
		return 0

	case wmMouseWheel:
		x, y := a.screenToClient(lo16(lp), hi16(lp))
		if a.rOpts.has(x, y) {
			a.scrollOptions(-hi16(wp) * a.scale(48) / 120)
		}
		return 0

	case wmCommand:
		if hi16(wp) == 0x300 /* EN_CHANGE */ {
			a.onEditChange(lp)
			return 0
		}

	case wmNotify:
		hdr := (*nmhdr)(ptr(lp))
		if a.lv != nil && hdr.HwndFrom == a.lv.hwnd {
			if r, ok := a.onListNotify(lp); ok {
				return r
			}
		}

	case wmTimer:
		switch wp {
		case timerBusy:
			a.frame++
			a.lv.frame = a.frame
			a.lv.refreshRunning()
			invalidate(hwnd, &a.rAction)
		case timerToast:
			pKillTimer.Call(hwnd, timerToast)
			a.toast, a.toastAction = "", ""
			a.layout()
			invalidate(hwnd, nil)
		case timerFlush:
			pKillTimer.Call(hwnd, timerFlush)
			files := a.pending
			a.pending = nil
			a.addFiles(files, true)
		case timerShot:
			a.onShotTimer()
		}
		return 0

	case wmDropFiles:
		files := dropFiles(wp)
		pDragFinish.Call(wp)
		a.addFiles(files, true)
		bringToFront(hwnd)
		return 0

	case wmCopyData:
		if files, ok := readCopyData(lp); ok {
			a.pending = append(a.pending, files...)
			pSetTimer.Call(hwnd, timerFlush, 300, 0)
			bringToFront(hwnd)
			return 1
		}

	case wmTaskUpdate:
		a.dirty.Store(false)
		a.onTaskUpdate()
		return 0

	case wmGPUReady:
		a.layout()
		invalidate(hwnd, nil)
		return 0

	case wmClose:
		if n := a.runner.Busy(); n > 0 {
			r, _ := (&taskDialog{title: appName, instruction: "还有文件正在转换，确定要退出吗？",
				content: "退出后正在转换的文件会停下，转了一半的结果会被删掉。", icon: tdWarningIcon,
				buttons: []tdButton{{idOK, "退出"}}, cancel: true, defaultBtn: idCancel}).show(hwnd)
			if r != idOK {
				return 0
			}
			a.runner.CancelAll()
			// 等正在运行的任务收尾（删掉半成品），最多 3 秒
			for i := 0; i < 60 && a.runner.Busy() > 0; i++ {
				time.Sleep(50 * time.Millisecond)
			}
		}
		pDestroyWindow.Call(hwnd)
		return 0

	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func (a *app) screenToClient(x, y int32) (int32, int32) {
	p := point{x, y}
	pScreenToClient.Call(a.hwnd, uintptr(unsafe.Pointer(&p)))
	return p.X, p.Y
}

// preTranslate 处理全局快捷键
func (a *app) preTranslate(m *msg) bool {
	if m.Message != wmKeyDown {
		return false
	}
	if _, isEdit := a.editKey(m.Hwnd); isEdit {
		if m.WParam == vkReturn {
			pSetFocus.Call(a.hwnd)
			return true
		}
		return false
	}
	switch {
	case m.WParam == 'V' && keyDown(vkCtrl):
		if files := clipboardFiles(a.hwnd); len(files) > 0 {
			a.addFiles(files, true)
		}
		return true
	case m.WParam == 'O' && keyDown(vkCtrl):
		a.addFilesDialog()
		return true
	case m.WParam == vkReturn && keyDown(vkCtrl):
		a.startOrStop()
		return true
	}
	return false
}

func (a *app) onMouseMove(x, y int32) {
	if !a.mouseIn {
		a.mouseIn = true
		tme := trackMouseEvent{Flags: 0x2, HwndTrack: a.hwnd}
		tme.Size = uint32(unsafe.Sizeof(tme))
		pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
	}
	a.mouse = point{x, y}
	a.setHot(a.hitTest(x, y))
}

func (a *app) hitTest(x, y int32) string {
	for i := len(a.scene) - 1; i >= 0; i-- {
		w := &a.scene[i]
		if w.click == nil || !w.enabled || !w.r.has(x, y) {
			continue
		}
		if w.clip.W() > 0 && !w.clip.has(x, y) {
			continue
		}
		return w.id
	}
	return ""
}

func (a *app) setHot(id string) {
	if id == a.hotID {
		return
	}
	a.hotID = id
	invalidate(a.hwnd, nil)
}

func (a *app) click(id string) {
	for i := range a.scene {
		if a.scene[i].id == id && a.scene[i].click != nil {
			a.scene[i].click()
			return
		}
	}
}

// ---------------------------------------------------------------- 页面、目标、选项

func (a *app) switchPage(i int) {
	if i == a.cur || i < 0 || i >= len(a.pages) {
		return
	}
	a.cur = i
	a.optScroll = 0
	a.lv.setItems(a.page().items, a.page().target)
	a.syncEdits()
	a.layout()
	invalidate(a.hwnd, nil)
}

func (a *app) chooseTarget(t *catalog.Target) {
	ps := a.page()
	ps.target = t
	t.Defaults(ps.opts)
	a.lv.setItems(ps.items, t)
	a.syncEdits()
	a.layout()
	invalidate(a.hwnd, nil)
}

func (a *app) setOpt(key, value string) {
	a.page().opts[key] = value
	a.layout()
	invalidate(a.hwnd, nil)
}

// pickChoice 弹出下拉菜单让用户选一项
func (a *app) pickChoice(f catalog.Field, r rect) {
	ps := a.page()
	menu, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(menu)
	cur := ps.opts[f.Key]
	for i, c := range f.Choices {
		flags := uintptr(0)
		if c.Value == cur && f.Apply == nil {
			flags |= 0x8 // MF_CHECKED
		}
		pAppendMenuW.Call(menu, flags, uintptr(i+1), uintptr(unsafe.Pointer(u16(c.Label))))
	}
	p := point{r.Left, r.Bottom + a.scale(2)}
	pClientToScreen.Call(a.hwnd, uintptr(unsafe.Pointer(&p)))
	cmd, _, _ := pTrackPopupMenuEx.Call(menu, 0x0100 /* TPM_RETURNCMD */, uintptr(p.X), uintptr(p.Y), a.hwnd, 0)
	if cmd == 0 {
		return
	}
	c := f.Choices[cmd-1]
	if f.Apply != nil {
		if c.Value != "" {
			f.Apply(ps.opts, c.Value)
			a.syncEdits()
		}
	} else {
		ps.opts[f.Key] = c.Value
	}
	a.layout()
	invalidate(a.hwnd, nil)
}

// ---------------------------------------------------------------- 选项输入框

func (a *app) editFor(f catalog.Field) uintptr {
	if h, ok := a.edits[f.Key]; ok {
		return h
	}
	const esAutoHScroll, esNumber, esPassword = 0x80, 0x2000, 0x20
	style := uintptr(wsChild | wsTabStop | esAutoHScroll)
	if f.Type == catalog.Number {
		style |= esNumber
	}
	if f.Type == catalog.Password {
		style |= esPassword
	}
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("EDIT"))), 0, style, 0, 0, 10, 10, a.hwnd, 0, moduleHandle(), 0)
	sendMessage(h, wmSetFont, a.f.ui, 1)
	if f.Placeholder != "" {
		sendMessage(h, 0x1501 /* EM_SETCUEBANNER */, 1, uintptr(unsafe.Pointer(u16(f.Placeholder))))
	}
	sendMessage(h, 0xC5 /* EM_LIMITTEXT */, 200, 0)
	proc, _, _ := pSetWindowLongPtrW.Call(h, ^uintptr(3), editSubclass)
	a.editProc = proc
	a.edits[f.Key] = h
	a.syncing = true
	setWindowText(h, a.page().opts[f.Key])
	a.syncing = false
	return h
}

var editSubclass = syscall.NewCallback(func(hwnd, m, wp, lp uintptr) uintptr {
	a := theApp
	if m == wmChar && (wp == '\r' || wp == '\n') {
		return 0 // 回车不要「叮」一声
	}
	r, _, _ := pCallWindowProcW.Call(a.editProc, hwnd, m, wp, lp)
	return r
})

func (a *app) editKey(hwnd uintptr) (string, bool) {
	for k, h := range a.edits {
		if h == hwnd {
			return k, true
		}
	}
	return "", false
}

func (a *app) onEditChange(hwnd uintptr) {
	if a.syncing {
		return
	}
	if k, ok := a.editKey(hwnd); ok {
		a.page().opts[k] = strings.TrimSpace(windowText(hwnd))
		invalidate(a.hwnd, &a.rAction)
	}
}

// syncEdits 把当前页的选项值填进输入框（换页、换目标、选了预设之后）
func (a *app) syncEdits() {
	a.syncing = true
	defer func() { a.syncing = false }()
	for k, h := range a.edits {
		v := a.page().opts[k]
		if windowText(h) != v {
			setWindowText(h, v)
		}
	}
}

func (a *app) scrollOptions(dy int32) {
	maxScroll := max(0, a.optHeight-a.rOptsBody.H())
	n := min(max(a.optScroll+dy, 0), maxScroll)
	if n == a.optScroll {
		return
	}
	a.optScroll = n
	a.layout()
	invalidate(a.hwnd, nil)
}

// ---------------------------------------------------------------- 添加文件

const maxFolderFiles = 5000

// addFiles 把文件（或文件夹里的所有文件）加进列表。能放进当前页的放当前页，
// 其余的按类型放进对应的页；如果当前页一个都放不了，就切到放得最多的那一页。
func (a *app) addFiles(paths []string, switchPage bool) {
	var files []string
	tooMany := false
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "$") {
					if path != p {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if _, ok := conv.KindOf(path); ok {
				if len(files) >= maxFolderFiles {
					tooMany = true
					return filepath.SkipAll
				}
				files = append(files, path)
			}
			return nil
		})
	}
	added := make([]int, len(a.pages))
	unknown, dup := 0, 0
	curFirst := false
	for _, f := range files {
		dest := -1
		if a.page().page.Accepts(f) {
			dest = a.cur
		} else if k, ok := conv.KindOf(f); ok {
			for i, ps := range a.pages {
				if ps.page.Kind == k {
					dest = i
				}
			}
		}
		if dest < 0 {
			unknown++
			continue
		}
		ps := a.pages[dest]
		if ps.has(f) {
			dup++
			continue
		}
		var size int64
		if fi, err := os.Stat(f); err == nil {
			size = fi.Size()
		}
		ps.items = append(ps.items, &item{path: f, size: size})
		added[dest]++
		if dest == a.cur {
			curFirst = true
		}
	}
	total := 0
	best := a.cur
	for i, n := range added {
		total += n
		if n > added[best] {
			best = i
		}
	}
	if switchPage && !curFirst && total > 0 {
		a.cur = best
		a.optScroll = 0
		a.syncEdits()
	}
	a.lv.setItems(a.page().items, a.page().target)
	// 提示
	var parts []string
	if total > 0 {
		msg := "已添加 " + itoa(total) + " 个文件"
		var where []string
		for i, n := range added {
			if n > 0 && i != a.cur {
				where = append(where, a.pages[i].page.Kind.Name()+" "+itoa(n)+" 个")
			}
		}
		if len(where) > 0 {
			msg += "（其中" + strings.Join(where, "、") + "放在对应的页里）"
		}
		parts = append(parts, msg)
	}
	if dup > 0 {
		parts = append(parts, itoa(dup)+" 个已经在列表里了")
	}
	if unknown > 0 {
		parts = append(parts, itoa(unknown)+" 个文件不认识，跳过了")
	}
	if tooMany {
		parts = append(parts, "文件夹里文件太多，只加了前 "+itoa(maxFolderFiles)+" 个")
	}
	if len(parts) > 0 {
		a.showToast(strings.Join(parts, "，"), total == 0, "")
	}
	a.layout()
	invalidate(a.hwnd, nil)
}

func (ps *pageState) has(path string) bool {
	for _, it := range ps.items {
		if strings.EqualFold(it.path, path) {
			return true
		}
	}
	return false
}

func (a *app) addFilesDialog() {
	p := a.page().page
	exts := p.Exts()
	sort.Strings(exts)
	var spec []string
	for _, e := range exts {
		spec = append(spec, "*."+e)
	}
	files := pickFiles("选择要转换的"+p.Kind.Name()+"文件", []fileFilter{
		{p.Kind.Name() + "文件", strings.Join(spec, ";")},
		{"所有文件", "*.*"},
	})
	if len(files) > 0 {
		a.addFiles(files, true)
	}
}

func (a *app) clearList() {
	ps := a.page()
	if a.pageBusy(ps) {
		r, _ := (&taskDialog{title: appName, instruction: "清空列表会停止正在进行的转换，确定吗？",
			icon: tdWarningIcon, buttons: []tdButton{{idOK, "停止并清空"}}, cancel: true, defaultBtn: idCancel}).show(a.hwnd)
		if r != idOK {
			return
		}
		for _, it := range ps.items {
			if it.task != nil {
				a.runner.Cancel(it.task)
			}
		}
	}
	ps.items = nil
	a.lv.setItems(nil, ps.target)
	a.layout()
	invalidate(a.hwnd, nil)
}

func (a *app) pageBusy(ps *pageState) bool {
	for _, it := range ps.items {
		if it.task != nil {
			if s := a.runner.Snapshot(it.task).State; s == conv.Waiting || s == conv.Running {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- 开始转换

// startOrStop 当前页有任务在跑时停止这一页的任务，否则开始转换（别的页的任务不受影响）
func (a *app) startOrStop() {
	ps := a.page()
	if a.pageBusy(ps) {
		for _, it := range ps.items {
			if it.task != nil {
				a.runner.Cancel(it.task)
			}
		}
		return
	}
	a.start(nil)
}

func (a *app) runKey(t *catalog.Target, opt conv.Options) string {
	return t.ID + "|" + opt.String() + "|" + a.outMode + "|" + a.outDir
}

// start 开始转换当前页列表里还没转过的文件；only 不为 nil 时只转这一个
func (a *app) start(only *item) {
	ps := a.page()
	t := ps.target
	if len(ps.items) == 0 {
		a.addFilesDialog()
		return
	}
	opt := t.Prepare(ps.opts)
	if msg := t.Check(opt); msg != "" {
		showMessage(a.hwnd, tdWarningIcon, appName, msg, "")
		return
	}
	if a.outMode == "custom" {
		if err := os.MkdirAll(a.outDir, 0o755); err != nil || !writable(a.outDir) {
			showMessage(a.hwnd, tdErrorIcon, appName, "没法把结果保存到这个文件夹", a.outDir+"\n\n请在下面的「保存到」里换一个文件夹。")
			return
		}
	}
	key := a.runKey(t, opt)
	var todo []*item
	accepted := 0
	for _, it := range ps.items {
		if !t.Accept(it.path) || (only != nil && it != only) {
			continue
		}
		accepted++
		if it.task != nil {
			s := a.runner.Snapshot(it.task).State
			if s == conv.Waiting || s == conv.Running || (s == conv.Done && it.key == key) {
				continue
			}
		}
		todo = append(todo, it)
	}
	switch {
	case accepted == 0:
		showMessage(a.hwnd, tdWarningIcon, appName, "列表里没有能转成「"+t.Label+"」的文件", "换一个目标格式，或者添加别的文件。")
		return
	case len(todo) == 0:
		a.showToast("这些文件已经用同样的设置转换过了；改了设置再点就会重新转换", false, "")
		return
	}
	if t.ID == "pdf:merge" && len(todo) < 2 {
		showMessage(a.hwnd, tdWarningIcon, appName, "至少要两个 PDF 才能合并", "")
		return
	}
	sess := a.sess
	if sess == nil {
		sess = &session{page: a.cur, started: time.Now()}
	}
	if t.Batch {
		var paths []string
		for _, it := range todo {
			paths = append(paths, it.path)
		}
		task := &conv.Task{Job: conv.NewJob(paths, t.ID, opt, a.outDirFor(paths[0]), nil), Lane: t.Lane, Run: t.Run}
		for _, it := range todo {
			it.task, it.key, it.outText = task, key, ""
		}
		sess.tasks = append(sess.tasks, task)
		a.runner.Submit(task)
	} else {
		for _, it := range todo {
			task := &conv.Task{Job: conv.NewJob([]string{it.path}, t.ID, opt.Clone(), a.outDirFor(it.path), nil), Lane: t.Lane, Run: t.Run, Tag: it}
			it.task, it.key, it.outText = task, key, ""
			sess.tasks = append(sess.tasks, task)
			a.runner.Submit(task)
		}
	}
	a.sess = sess
	a.toast = ""
	keepAwake(true)
	pSetTimer.Call(a.hwnd, timerBusy, 120, 0)
	a.saveSettings()
	a.layout()
	invalidate(a.hwnd, nil)
}

// outDirFor 决定输出文件放哪：默认和原文件放在一起；那里不能写时放到「文档\万能格式转换」
func (a *app) outDirFor(input string) string {
	if a.outMode == "custom" && a.outDir != "" {
		return a.outDir
	}
	dir := filepath.Dir(input)
	if writable(dir) {
		return dir
	}
	d := defaultOutDir()
	os.MkdirAll(d, 0o755)
	return d
}

func (a *app) onTaskUpdate() {
	a.lv.refresh()
	invalidate(a.hwnd, &a.rAction)
	if a.sess == nil {
		return
	}
	if a.runner.Busy() > 0 {
		// 任务栏上也能看到进度
		if st := a.statusText(); st != "" {
			setWindowText(a.hwnd, st+" · "+appName)
		}
		setTaskbarProgress(a.hwnd, a.overallProgress(), false)
		return
	}
	// 这一批全部结束
	pKillTimer.Call(a.hwnd, timerBusy)
	keepAwake(false)
	setWindowText(a.hwnd, appName)
	setTaskbarProgress(a.hwnd, -1, false)
	ok, failed, stopped := 0, 0, 0
	var outs []string
	seen := map[*conv.Task]bool{}
	for _, t := range a.sess.tasks {
		if seen[t] {
			continue
		}
		seen[t] = true
		info := a.runner.Snapshot(t)
		switch info.State {
		case conv.Done:
			ok += len(t.Job.Inputs)
			outs = append(outs, info.Outputs...)
		case conv.Failed:
			failed += len(t.Job.Inputs)
		default:
			stopped += len(t.Job.Inputs)
		}
	}
	a.sess = nil
	a.lv.refresh()
	var msg string
	switch {
	case failed == 0 && stopped == 0:
		msg = "全部完成，转换了 " + itoa(ok) + " 个文件"
	case ok == 0 && failed == 0:
		msg = "已停止"
	default:
		msg = "完成 " + itoa(ok) + " 个"
		if failed > 0 {
			msg += "，失败 " + itoa(failed) + " 个（原因见列表）"
		}
		if stopped > 0 {
			msg += "，停止 " + itoa(stopped) + " 个"
		}
	}
	action := ""
	if len(outs) > 0 {
		action = outs[0]
	}
	a.showToast(msg, failed > 0 && ok == 0, action)
	flashWindow(a.hwnd)
	a.layout()
	invalidate(a.hwnd, nil)
}

func (a *app) showToast(msg string, isErr bool, action string) {
	a.toast, a.toastErr, a.toastAction = msg, isErr, action
	d := uintptr(4500)
	if action != "" {
		d = 12000
	}
	pSetTimer.Call(a.hwnd, timerToast, d, 0)
	a.layout()
	invalidate(a.hwnd, nil)
}

func (a *app) chooseOutDir() {
	menu, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(menu)
	flag := func(on bool) uintptr {
		if on {
			return 0x8
		}
		return 0
	}
	pAppendMenuW.Call(menu, flag(a.outMode == "source"), 1, uintptr(unsafe.Pointer(u16("和原文件放在同一个文件夹"))))
	if a.outDir != "" {
		pAppendMenuW.Call(menu, flag(a.outMode == "custom"), 2, uintptr(unsafe.Pointer(u16(a.outDir))))
	}
	pAppendMenuW.Call(menu, 0x800 /* MF_SEPARATOR */, 0, 0)
	pAppendMenuW.Call(menu, 0, 3, uintptr(unsafe.Pointer(u16("选择文件夹…"))))
	if a.outMode == "custom" && a.outDir != "" {
		pAppendMenuW.Call(menu, 0, 4, uintptr(unsafe.Pointer(u16("打开这个文件夹"))))
	}
	var r rect
	for _, w := range a.scene {
		if w.id == "outdir" {
			r = w.r
		}
	}
	p := point{r.Left, r.Top}
	pClientToScreen.Call(a.hwnd, uintptr(unsafe.Pointer(&p)))
	cmd, _, _ := pTrackPopupMenuEx.Call(menu, 0x0100|0x0020 /* TPM_RETURNCMD | TPM_BOTTOMALIGN */, uintptr(p.X), uintptr(p.Y-a.scale(2)), a.hwnd, 0)
	switch cmd {
	case 1:
		a.outMode = "source"
	case 2:
		a.outMode = "custom"
	case 3:
		if d := pickFolder("选择保存转换结果的文件夹", a.outDir); d != "" {
			a.outMode, a.outDir = "custom", d
		}
	case 4:
		shellOpen(a.outDir)
	}
	a.saveSettings()
	a.layout()
	invalidate(a.hwnd, nil)
}

func (a *app) toggleContextMenu() {
	var err error
	if a.menuOn {
		err = uninstallContextMenu()
	} else {
		err = installContextMenu()
	}
	if err != nil {
		showMessage(a.hwnd, tdErrorIcon, appName, "没能修改右键菜单", err.Error())
		return
	}
	a.menuOn = contextMenuInstalled()
	if a.menuOn {
		a.showToast("已添加：在文件上点右键 →「用万能格式转换打开」（Windows 11 要先点「显示更多选项」）", false, "")
	} else {
		a.showToast("已从右键菜单里去掉", false, "")
	}
}

func itoa(n int) string { return formatCount(int64(n)) }

// overallProgress 这一批任务的总进度（0~1）
func (a *app) overallProgress() float64 {
	if a.sess == nil {
		return 0
	}
	seen := map[*conv.Task]bool{}
	var sum float64
	n := 0
	for _, t := range a.sess.tasks {
		if seen[t] {
			continue
		}
		seen[t] = true
		n++
		info := a.runner.Snapshot(t)
		switch info.State {
		case conv.Done, conv.Failed, conv.Cancelled:
			sum++
		case conv.Running:
			sum += max(info.Progress.Frac, 0)
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// keepAwake 转换期间不让电脑自动睡眠（屏幕照常可以关）
func keepAwake(on bool) {
	const esContinuous, esSystemRequired = 0x80000000, 0x1
	flags := uintptr(esContinuous)
	if on {
		flags |= esSystemRequired
	}
	kernel32.NewProc("SetThreadExecutionState").Call(flags)
}
