//go:build windows

package main

// 右侧的文件列表：系统自带的 ListView（虚拟模式），「状态」一栏自己画进度条。

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
)

const (
	lvmSetImageList     = 0x1003
	lvmGetNextItem      = 0x100C
	lvmEnsureVisible    = 0x1013
	lvmRedrawItems      = 0x1015
	lvmSetColumnWidth   = 0x101E
	lvmGetTopIndex      = 0x1027
	lvmGetCountPerPage  = 0x1028
	lvmSetItemState     = 0x102B
	lvmSetItemCount     = 0x102F
	lvmSetExtendedStyle = 0x1036
	lvmGetSubItemRect   = 0x1038
	lvmInsertColumnW    = 0x1061
	lvnGetDispInfoW     = ^uint32(176) // -177
	lvnKeyDown          = ^uint32(154) // -155
	lvnOdFindItemW      = ^uint32(178) // -179
	nmDblClk            = ^uint32(2)   // -3
	nmRClick            = ^uint32(4)   // -5
	nmCustomDraw        = ^uint32(11)  // -12
	cddsPrepaint        = 0x1
	cddsItemPrepaint    = 0x10001
	cddsSubItemPrepaint = 0x30001
	cdrfDoDefault       = 0x0
	cdrfSkipDefault     = 0x4
	cdrfNewFont         = 0x2
	cdrfNotifyItemDraw  = 0x20
	cdrfNotifySubItem   = 0x20
	lvisSelected        = 0x2
	lvisFocused         = 0x1
)

type lvColumn struct {
	Mask      uint32
	Fmt       int32
	Cx        int32
	Text      *uint16
	TextMax   int32
	SubItem   int32
	Image     int32
	Order     int32
	CxMin     int32
	CxDefault int32
	CxIdeal   int32
}

type lvItem struct {
	Mask      uint32
	Item      int32
	SubItem   int32
	State     uint32
	StateMask uint32
	Text      *uint16
	TextMax   int32
	Image     int32
	Param     uintptr
	Indent    int32
	GroupID   int32
	Columns   uint32
	PuColumns uintptr
	PiColFmt  uintptr
	Group     int32
}

type nmLVDispInfo struct {
	Hdr  nmhdr
	Item lvItem
}

type nmLVKeyDown struct {
	Hdr   nmhdr
	VKey  uint16
	Flags uint32
}

type nmItemActivate struct {
	Hdr      nmhdr
	Item     int32
	SubItem  int32
	NewState uint32
	OldState uint32
	Changed  uint32
	Action   point
	Param    uintptr
	KeyFlags uint32
}

type nmLVCustomDraw struct {
	Hdr       nmhdr
	DrawStage uint32
	Hdc       uintptr
	Rc        rect
	ItemSpec  uintptr
	ItemState uint32
	ItemParam uintptr
	TextColor uint32
	TextBk    uint32
	SubItem   int32
}

type fileList struct {
	hwnd   uintptr
	items  []*item
	target *catalog.Target
	icons  map[string]int32
	scale  float64
	font   uintptr
	frame  int
}

const (
	colName = iota
	colSize
	colState
	colResult
)

func newFileList(parent uintptr, font uintptr, scale float64) *fileList {
	const (
		lvsReport, lvsShowSelAlways, lvsShareImageLists, lvsOwnerData = 0x1, 0x8, 0x40, 0x1000
		lvsExFullRowSelect, lvsExDoubleBuffer, lvsExLabelTip          = 0x20, 0x10000, 0x4000
	)
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("SysListView32"))), 0,
		wsChild|wsTabStop|lvsReport|lvsShowSelAlways|lvsShareImageLists|lvsOwnerData,
		0, 0, 100, 100, parent, 100, moduleHandle(), 0)
	lv := &fileList{hwnd: hwnd, icons: map[string]int32{}, scale: scale, font: font}
	pSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(u16("Explorer"))), 0)
	sendMessage(hwnd, lvmSetExtendedStyle, 0, lvsExFullRowSelect|lvsExDoubleBuffer|lvsExLabelTip)
	sendMessage(hwnd, wmSetFont, font, 0)
	var sfi shFileInfo
	il, _, _ := pSHGetFileInfoW.Call(uintptr(unsafe.Pointer(u16("C:\\"))), 0, uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi), shgfiSysIconIndex|shgfiSmallIcon)
	if il != 0 {
		sendMessage(hwnd, lvmSetImageList, 1, il)
	}
	cols := []struct {
		name  string
		width int32
		right bool
	}{{"文件", 220, false}, {"大小", 76, true}, {"状态", 120, false}, {"结果", 240, false}}
	for i, c := range cols {
		col := lvColumn{Mask: 0x1 | 0x2 | 0x4 | 0x8, Cx: int32(float64(c.width) * scale), Text: u16(c.name), SubItem: int32(i)}
		if c.right {
			col.Fmt = 1
		}
		sendMessage(hwnd, lvmInsertColumnW, uintptr(i), uintptr(unsafe.Pointer(&col)))
	}
	return lv
}

func (lv *fileList) setFont(font uintptr, scale float64) {
	lv.font, lv.scale = font, scale
	sendMessage(lv.hwnd, wmSetFont, font, 1)
}

func (lv *fileList) fitColumns(width int32) {
	s := lv.scale
	scroll := int32(22 * s)
	size, state := int32(76*s), int32(124*s)
	rest := max(width-size-state-scroll, int32(240*s))
	name := rest * 46 / 100
	sendMessage(lv.hwnd, lvmSetColumnWidth, colName, uintptr(name))
	sendMessage(lv.hwnd, lvmSetColumnWidth, colSize, uintptr(size))
	sendMessage(lv.hwnd, lvmSetColumnWidth, colState, uintptr(state))
	sendMessage(lv.hwnd, lvmSetColumnWidth, colResult, uintptr(rest-name))
}

func (lv *fileList) setItems(items []*item, t *catalog.Target) {
	samePage := len(items) > 0 && len(lv.items) > 0 && &items[0] == &lv.items[0]
	lv.items, lv.target = items, t
	if !samePage {
		// 虚拟列表按序号记住选中的行，换了一批文件后要清掉，免得选中别的页的文件
		lv.selectOnly(-1)
	}
	sendMessage(lv.hwnd, lvmSetItemCount, uintptr(len(items)), 0x2 /* LVSICF_NOSCROLL */)
	invalidate(lv.hwnd, nil)
}

func (lv *fileList) refresh() { invalidate(lv.hwnd, nil) }

// refreshRunning 只重画可见范围（转圈动画用）
func (lv *fileList) refreshRunning() {
	top := sendMessage(lv.hwnd, lvmGetTopIndex, 0, 0)
	n := sendMessage(lv.hwnd, lvmGetCountPerPage, 0, 0)
	sendMessage(lv.hwnd, lvmRedrawItems, top, top+n+1)
}

func (lv *fileList) selection() []int {
	var out []int
	i := ^uintptr(0)
	for {
		r := sendMessage(lv.hwnd, lvmGetNextItem, i, 0x2 /* LVNI_SELECTED */)
		if int32(r) < 0 {
			return out
		}
		out = append(out, int(int32(r)))
		i = r
	}
}

func (lv *fileList) selectOnly(idx int) {
	item := lvItem{StateMask: lvisSelected | lvisFocused}
	sendMessage(lv.hwnd, lvmSetItemState, ^uintptr(0), uintptr(unsafe.Pointer(&item)))
	if idx >= 0 && idx < len(lv.items) {
		item.State = lvisSelected | lvisFocused
		sendMessage(lv.hwnd, lvmSetItemState, uintptr(idx), uintptr(unsafe.Pointer(&item)))
		sendMessage(lv.hwnd, lvmEnsureVisible, uintptr(idx), 0)
	}
}

func (lv *fileList) selectAll() {
	item := lvItem{StateMask: lvisSelected, State: lvisSelected}
	sendMessage(lv.hwnd, lvmSetItemState, ^uintptr(0), uintptr(unsafe.Pointer(&item)))
}

// toneRGB 把文字的颜色换成具体的色值
func toneRGB(t tone) rgb {
	switch t {
	case toneText2:
		return cText2
	case toneText3:
		return cText3
	case toneAccent:
		return cAccent
	case toneOK:
		return cOK
	case toneDanger:
		return cDanger
	}
	return cText
}

// ---------------------------------------------------------------- 通知

func (a *app) onListNotify(lp uintptr) (uintptr, bool) {
	lv := a.lv
	hdr := (*nmhdr)(ptr(lp))
	switch hdr.Code {
	case lvnGetDispInfoW:
		di := (*nmLVDispInfo)(ptr(lp))
		i := int(di.Item.Item)
		if i < 0 || i >= len(lv.items) {
			return 0, true
		}
		it := lv.items[i]
		if di.Item.Mask&0x1 != 0 && di.Item.Text != nil && di.Item.TextMax > 0 {
			var s string
			switch di.Item.SubItem {
			case colName:
				s = filepath.Base(it.path)
			case colSize:
				s = humanSize(it.size)
			case colState:
				s = "" // 自己画
			case colResult:
				s = a.rowResult(it)
			}
			dst := unsafe.Slice(di.Item.Text, di.Item.TextMax)
			src, _ := windows.UTF16FromString(s)
			n := copy(dst, src)
			dst[min(n, len(dst)-1)] = 0
		}
		if di.Item.Mask&0x2 != 0 {
			di.Item.Image = lv.iconFor(it.path)
		}
		return 0, true

	case lvnOdFindItemW:
		return ^uintptr(0), true

	case nmCustomDraw:
		cd := (*nmLVCustomDraw)(ptr(lp))
		switch cd.DrawStage {
		case cddsPrepaint:
			return cdrfNotifyItemDraw, true
		case cddsItemPrepaint:
			return cdrfNotifySubItem, true
		case cddsSubItemPrepaint:
			i := int(cd.ItemSpec)
			if i < 0 || i >= len(lv.items) {
				return cdrfDoDefault, true
			}
			it := lv.items[i]
			name, result := a.rowTones(it)
			switch cd.SubItem {
			case colState:
				a.drawState(cd, it)
				return cdrfSkipDefault, true
			case colResult:
				cd.TextColor = uint32(toneRGB(result).colorref())
				return cdrfNewFont, true
			default:
				cd.TextColor = uint32(toneRGB(name).colorref())
				return cdrfNewFont, true
			}
		}
		return cdrfDoDefault, true

	case nmDblClk:
		ia := (*nmItemActivate)(ptr(lp))
		if ia.Item >= 0 && int(ia.Item) < len(lv.items) {
			a.openItem(lv.items[ia.Item])
		}
		return 0, true

	case nmRClick:
		ia := (*nmItemActivate)(ptr(lp))
		if ia.Item >= 0 {
			a.itemMenu()
		}
		return 0, true

	case lvnKeyDown:
		kd := (*nmLVKeyDown)(ptr(lp))
		switch kd.VKey {
		case vkDelete:
			a.removeSelected()
		case 'A':
			if keyDown(vkCtrl) {
				lv.selectAll()
			}
		case 'V':
			if keyDown(vkCtrl) {
				if files := clipboardFiles(a.hwnd); len(files) > 0 {
					a.addFiles(files, true)
				}
			}
		}
		return 0, true
	}
	return 0, false
}

// drawState 在「状态」一栏画文字或进度条
func (a *app) drawState(cd *nmLVCustomDraw, it *item) {
	lv := a.lv
	r := rect{Top: colState, Left: 0}
	sendMessage(lv.hwnd, lvmGetSubItemRect, cd.ItemSpec, uintptr(unsafe.Pointer(&r)))
	if r.W() <= 0 {
		return
	}
	selected := cd.ItemState&0x1 != 0 // CDIS_SELECTED
	_ = selected
	s := lv.scale
	label, tn, frac, running := a.rowState(it)
	color := toneRGB(tn)
	pad := int32(6 * s)
	if !running {
		textLine(cd.Hdc, lv.font, label, rect{r.Left + pad, r.Top, r.Right - pad, r.Bottom}, color, dtLeft)
		return
	}
	h := int32(6 * s)
	barW := r.W() - pad*2 - int32(40*s)
	bar := rect{r.Left + pad, r.Top + (r.H()-h)/2, r.Left + pad + max(barW, int32(20*s)), r.Top + (r.H()-h)/2 + h}
	g := newGP(cd.Hdc)
	g.roundRect(bar, h/2, 0xE3E8F0, 255)
	if frac >= 0 {
		if w := int32(float64(bar.W()) * min(frac, 1)); w > 0 {
			g.roundRect(rect{bar.Left, bar.Top, bar.Left + max(w, h), bar.Bottom}, h/2, cAccent, 255)
		}
	} else {
		// 说不准进度时，一小段蓝色来回跑
		seg := bar.W() / 3
		span := bar.W() - seg
		pos := int32(lv.frame*int(4*s)) % (2 * max(span, 1))
		if pos > span {
			pos = 2*span - pos
		}
		g.roundRect(rect{bar.Left + pos, bar.Top, bar.Left + pos + seg, bar.Bottom}, h/2, cAccent, 255)
	}
	g.close()
	pct := "…"
	if frac >= 0 {
		pct = fmt.Sprintf("%d%%", int(frac*100))
	}
	textLine(cd.Hdc, lv.font, pct, rect{bar.Right + int32(6*s), r.Top, r.Right - pad/2, r.Bottom}, cText2, dtLeft)
}

func (lv *fileList) iconFor(path string) int32 {
	ext := strings.ToLower(filepath.Ext(path))
	if idx, ok := lv.icons[ext]; ok {
		return idx
	}
	var sfi shFileInfo
	pSHGetFileInfoW.Call(uintptr(unsafe.Pointer(u16("x"+ext))), 0x80, uintptr(unsafe.Pointer(&sfi)), unsafe.Sizeof(sfi),
		shgfiSysIconIndex|shgfiSmallIcon|shgfiUseFileAttributes)
	lv.icons[ext] = sfi.IconIndex
	return sfi.IconIndex
}

type shFileInfo struct {
	Icon        uintptr
	IconIndex   int32
	Attributes  uint32
	DisplayName [260]uint16
	TypeName    [80]uint16
}

const (
	shgfiSysIconIndex      = 0x4000
	shgfiSmallIcon         = 0x1
	shgfiUseFileAttributes = 0x10
)

// ---------------------------------------------------------------- 右键菜单和操作

// openItem 双击：转换完成的打开结果所在位置，否则打开原文件
func (a *app) openItem(it *item) {
	if it.task != nil {
		if info := a.runner.Snapshot(it.task); info.State == conv.Done && len(info.Outputs) > 0 {
			revealFile(info.Outputs[0])
			return
		}
		if info := a.runner.Snapshot(it.task); info.State == conv.Failed {
			a.showError(it, info.Err)
			return
		}
	}
	shellOpen(it.path)
}

func (a *app) showError(it *item, err error) {
	(&taskDialog{title: appName, instruction: filepath.Base(it.path) + " 没有转换成功", content: errText(err),
		expanded: errDetail(err), icon: tdErrorIcon, buttons: []tdButton{{idOK, "知道了"}}}).show(a.hwnd)
}

func (a *app) itemMenu() {
	sel := a.lv.selection()
	if len(sel) == 0 {
		return
	}
	it := a.lv.items[sel[0]]
	var info conv.TaskInfo
	if it.task != nil {
		info = a.runner.Snapshot(it.task)
	}
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
	menu, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(menu)
	add := func(id int, label string, enabled bool) {
		flags := uintptr(0)
		if !enabled {
			flags |= 0x1 // MF_GRAYED
		}
		pAppendMenuW.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(u16(label))))
	}
	sep := func() { pAppendMenuW.Call(menu, 0x800, 0, 0) }
	single := len(sel) == 1
	if single && info.State == conv.Done && len(info.Outputs) > 0 {
		add(cmOpenOut, "打开转换结果", true)
		add(cmRevealOut, "在文件夹中显示结果", true)
		sep()
	}
	if single && info.State == conv.Failed {
		add(cmError, "查看失败原因", true)
		add(cmCopyErr, "复制失败原因", true)
		sep()
	}
	add(cmOpen, "打开原文件", single)
	add(cmReveal, "在文件夹中显示原文件", single)
	sep()
	add(cmUp, "上移\tAlt+↑", single && sel[0] > 0)
	add(cmDown, "下移\tAlt+↓", single && sel[0] < len(a.lv.items)-1)
	sep()
	if info.State == conv.Running || info.State == conv.Waiting {
		add(cmStop, "停止", true)
	}
	if single && (info.State == conv.Done || info.State == conv.Failed || info.State == conv.Cancelled) {
		add(cmRedo, "重新转换", true)
	}
	add(cmRemove, "从列表中移除\tDelete", true)

	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	cmd, _, _ := pTrackPopupMenuEx.Call(menu, 0x0100, uintptr(p.X), uintptr(p.Y), a.hwnd, 0)
	switch int(cmd) {
	case cmOpen:
		shellOpen(it.path)
	case cmReveal:
		revealFile(it.path)
	case cmOpenOut:
		shellOpen(info.Outputs[0])
	case cmRevealOut:
		revealFile(info.Outputs[0])
	case cmError:
		a.showError(it, info.Err)
	case cmCopyErr:
		setClipboardText(a.hwnd, filepath.Base(it.path)+"："+info.Err.Error())
	case cmUp:
		a.moveSelected(-1)
	case cmDown:
		a.moveSelected(1)
	case cmStop:
		for _, i := range sel {
			if t := a.lv.items[i].task; t != nil {
				a.runner.Cancel(t)
			}
		}
	case cmRedo:
		a.start(a.prepareRedo(it)) // 合并出来的文件整批重新合并
	case cmRemove:
		a.removeSelected()
	}
}

func (a *app) moveSelected(d int) {
	sel := a.lv.selection()
	if len(sel) != 1 {
		return
	}
	i, j := sel[0], sel[0]+d
	if !a.moveItem(i, j) {
		return
	}
	ps := a.page()
	a.lv.setItems(ps.items, ps.target)
	a.lv.selectOnly(j)
}

func (a *app) removeSelected() {
	sel := a.lv.selection()
	if len(sel) == 0 {
		return
	}
	ps := a.page()
	// 合并任务里的文件被移除：整个合并停下来，调整好后重新开始
	if a.removeItems(sel) {
		a.showToast("已停止合并，调整好列表后再点「开始转换」", false, "")
	}
	keep := ps.items
	a.lv.setItems(keep, ps.target)
	a.lv.selectOnly(min(sel[0], len(keep)-1))
	a.layout()
	invalidate(a.hwnd, nil)
}
