//go:build windows

package main

// 系统的任务对话框（TaskDialog）：Windows 自己的确认框样式，支持自定义按钮文字、
// 警告图标和「我确定」勾选框。

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	tdWarningIcon = 0xFFFF // MAKEINTRESOURCE(-1)
	tdErrorIcon   = 0xFFFE
	tdInfoIcon    = 0xFFFD
	tdShieldIcon  = 0xFFFC

	idOK     = 1
	idCancel = 2
)

type tdButton struct {
	id   int32
	text string
}

type taskDialog struct {
	title       string
	instruction string // 粗体的大标题
	content     string
	expanded    string // 「详细信息」里折叠的内容
	verify      string // 勾选框文字
	footer      string
	icon        uint16
	buttons     []tdButton
	cancel      bool  // 加一个「取消」按钮
	defaultBtn  int32 // 默认按钮（按回车触发的那个）
	needVerify  int32 // 不为 0 时，这个按钮要先勾选勾选框才能点
}

// 回调只能注册有限个，全局用同一个
var (
	tdNeedVerify int32
	tdCallback   = syscall.NewCallback(func(hwnd, msg, wp, lp, ref uintptr) uintptr {
		const tdnCreated, tdnVerificationClicked, tdmEnableButton = 0, 8, 0x400 + 111
		switch msg {
		case tdnCreated:
			if tdNeedVerify != 0 {
				sendMessage(hwnd, tdmEnableButton, uintptr(tdNeedVerify), 0)
			}
		case tdnVerificationClicked:
			if tdNeedVerify != 0 {
				sendMessage(hwnd, tdmEnableButton, uintptr(tdNeedVerify), wp)
			}
		}
		return 0
	})
)

// show 显示对话框，返回点了哪个按钮（取消或关掉返回 idCancel）和勾选框的状态。
func (t *taskDialog) show(owner uintptr) (int32, bool) {
	if pTaskDialogIndirect.Find() != nil {
		return t.fallback(owner), false
	}
	var keep [][]uint16 // 字符串在调用结束前不能被回收
	str := func(s string) uint64 {
		if s == "" {
			return 0
		}
		p, _ := windows.UTF16FromString(s)
		keep = append(keep, p)
		return uint64(uintptr(unsafe.Pointer(&p[0])))
	}

	// TASKDIALOG_BUTTON 和 TASKDIALOGCONFIG 在头文件里是按 1 字节对齐的，只能手工排
	btns := make([]byte, 12*max(len(t.buttons), 1))
	for i, b := range t.buttons {
		binary.LittleEndian.PutUint32(btns[i*12:], uint32(b.id))
		binary.LittleEndian.PutUint64(btns[i*12+4:], str(b.text))
	}
	cfg := make([]byte, 160)
	le := binary.LittleEndian
	le.PutUint32(cfg[0:], 160)
	le.PutUint64(cfg[4:], uint64(owner))
	const tdfAllowCancel, tdfPositionRelative = 0x8, 0x1000
	le.PutUint32(cfg[20:], tdfAllowCancel|tdfPositionRelative)
	if t.cancel {
		le.PutUint32(cfg[24:], 0x8) // TDCBF_CANCEL_BUTTON
	}
	le.PutUint64(cfg[28:], str(t.title))
	le.PutUint64(cfg[36:], uint64(t.icon))
	le.PutUint64(cfg[44:], str(t.instruction))
	le.PutUint64(cfg[52:], str(t.content))
	le.PutUint32(cfg[60:], uint32(len(t.buttons)))
	if len(t.buttons) > 0 {
		le.PutUint64(cfg[64:], uint64(uintptr(unsafe.Pointer(&btns[0]))))
	}
	le.PutUint32(cfg[72:], uint32(t.defaultBtn))
	le.PutUint64(cfg[92:], str(t.verify))
	le.PutUint64(cfg[100:], str(t.expanded))
	if t.expanded != "" {
		le.PutUint64(cfg[108:], str("隐藏详细信息"))
		le.PutUint64(cfg[116:], str("显示详细信息"))
	}
	le.PutUint64(cfg[132:], str(t.footer))
	le.PutUint64(cfg[140:], uint64(tdCallback))

	tdNeedVerify = t.needVerify
	var button, radio, verified int32
	hr, _, _ := pTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg[0])),
		uintptr(unsafe.Pointer(&button)), uintptr(unsafe.Pointer(&radio)), uintptr(unsafe.Pointer(&verified)))
	tdNeedVerify = 0
	runtime.KeepAlive(keep)
	runtime.KeepAlive(btns)
	if hr != 0 {
		return t.fallback(owner), false
	}
	return button, verified != 0
}

// fallback 在没有任务对话框的老系统上用普通的消息框
func (t *taskDialog) fallback(owner uintptr) int32 {
	msg := t.instruction
	if t.content != "" {
		msg += "\n\n" + t.content
	}
	style := uint32(windows.MB_OK)
	if len(t.buttons) > 0 && t.cancel {
		style = windows.MB_OKCANCEL
	}
	switch t.icon {
	case tdWarningIcon:
		style |= windows.MB_ICONWARNING
	case tdErrorIcon:
		style |= windows.MB_ICONERROR
	default:
		style |= windows.MB_ICONINFORMATION
	}
	r, _ := windows.MessageBox(windows.HWND(owner), u16(msg), u16(t.title), style)
	if r == idOK && len(t.buttons) > 0 {
		return t.buttons[0].id
	}
	return idCancel
}

func showMessage(owner uintptr, icon uint16, title, instruction, content string) {
	if shotFile != "" { // 截图模式不弹窗，免得卡住
		println("提示：" + instruction + " " + content)
		return
	}
	(&taskDialog{title: title, instruction: instruction, content: content, icon: icon,
		buttons: []tdButton{{idOK, "知道了"}}}).show(owner)
}
