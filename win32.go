//go:build windows

package main

// 用到的 Win32 接口。只声明用得到的，结构体按 64 位 Windows 的内存布局写。

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	msimg32  = windows.NewLazySystemDLL("msimg32.dll")
	gdiplus  = windows.NewLazySystemDLL("gdiplus.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	uxtheme  = windows.NewLazySystemDLL("uxtheme.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pDestroyWindow                 = user32.NewProc("DestroyWindow")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pPostQuitMessage               = user32.NewProc("PostQuitMessage")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pSendMessageW                  = user32.NewProc("SendMessageW")
	pInvalidateRect                = user32.NewProc("InvalidateRect")
	pBeginPaint                    = user32.NewProc("BeginPaint")
	pEndPaint                      = user32.NewProc("EndPaint")
	pGetClientRect                 = user32.NewProc("GetClientRect")
	pGetDC                         = user32.NewProc("GetDC")
	pReleaseDC                     = user32.NewProc("ReleaseDC")
	pSetTimer                      = user32.NewProc("SetTimer")
	pKillTimer                     = user32.NewProc("KillTimer")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pSetCursor                     = user32.NewProc("SetCursor")
	pLoadImageW                    = user32.NewProc("LoadImageW")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pMoveWindow                    = user32.NewProc("MoveWindow")
	pCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                   = user32.NewProc("AppendMenuW")
	pSetMenuDefaultItem            = user32.NewProc("SetMenuDefaultItem")
	pTrackPopupMenuEx              = user32.NewProc("TrackPopupMenuEx")
	pDestroyMenu                   = user32.NewProc("DestroyMenu")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pScreenToClient                = user32.NewProc("ScreenToClient")
	pClientToScreen                = user32.NewProc("ClientToScreen")
	pTrackMouseEvent               = user32.NewProc("TrackMouseEvent")
	pGetKeyState                   = user32.NewProc("GetKeyState")
	pSetFocus                      = user32.NewProc("SetFocus")
	pGetFocus                      = user32.NewProc("GetFocus")
	pOpenClipboard                 = user32.NewProc("OpenClipboard")
	pEmptyClipboard                = user32.NewProc("EmptyClipboard")
	pSetClipboardData              = user32.NewProc("SetClipboardData")
	pCloseClipboard                = user32.NewProc("CloseClipboard")
	pMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	pSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW               = user32.NewProc("CallWindowProcW")
	pGetWindowTextW                = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW          = user32.NewProc("GetWindowTextLengthW")
	pSetWindowTextW                = user32.NewProc("SetWindowTextW")
	pDrawTextW                     = user32.NewProc("DrawTextW")
	pFillRect                      = user32.NewProc("FillRect")
	pSetCapture                    = user32.NewProc("SetCapture")
	pReleaseCapture                = user32.NewProc("ReleaseCapture")
	pIsWindowEnabled               = user32.NewProc("IsWindowEnabled")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pPrintWindow                   = user32.NewProc("PrintWindow")
	pGetDoubleClickTime            = user32.NewProc("GetDoubleClickTime")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pStretchBlt             = gdi32.NewProc("StretchBlt")
	pSetStretchBltMode      = gdi32.NewProc("SetStretchBltMode")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pSetBkColor             = gdi32.NewProc("SetBkColor")
	pGetStockObject         = gdi32.NewProc("GetStockObject")
	pSetDCBrushColor        = gdi32.NewProc("SetDCBrushColor")
	pGetTextExtentPoint32W  = gdi32.NewProc("GetTextExtentPoint32W")
	pCreateHatchBrush       = gdi32.NewProc("CreateHatchBrush")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pIntersectClipRect      = gdi32.NewProc("IntersectClipRect")
	pSelectClipRgn          = gdi32.NewProc("SelectClipRgn")
	pSaveDC                 = gdi32.NewProc("SaveDC")
	pRestoreDC              = gdi32.NewProc("RestoreDC")
	pGetTextFaceW           = gdi32.NewProc("GetTextFaceW")
	pGetDIBits              = gdi32.NewProc("GetDIBits")

	pGradientFill = msimg32.NewProc("GradientFill")

	pGdiplusStartup            = gdiplus.NewProc("GdiplusStartup")
	pGdipCreateFromHDC         = gdiplus.NewProc("GdipCreateFromHDC")
	pGdipDeleteGraphics        = gdiplus.NewProc("GdipDeleteGraphics")
	pGdipSetSmoothingMode      = gdiplus.NewProc("GdipSetSmoothingMode")
	pGdipSetPixelOffsetMode    = gdiplus.NewProc("GdipSetPixelOffsetMode")
	pGdipCreateSolidFill       = gdiplus.NewProc("GdipCreateSolidFill")
	pGdipDeleteBrush           = gdiplus.NewProc("GdipDeleteBrush")
	pGdipCreatePath            = gdiplus.NewProc("GdipCreatePath")
	pGdipDeletePath            = gdiplus.NewProc("GdipDeletePath")
	pGdipAddPathBeziersI       = gdiplus.NewProc("GdipAddPathBeziersI")
	pGdipClosePathFigure       = gdiplus.NewProc("GdipClosePathFigure")
	pGdipFillPath              = gdiplus.NewProc("GdipFillPath")
	pGdipFillEllipseI          = gdiplus.NewProc("GdipFillEllipseI")
	pGdipFillRectangleI        = gdiplus.NewProc("GdipFillRectangleI")
	pGdipCreateLineBrushI      = gdiplus.NewProc("GdipCreateLineBrushI")
	pGdipSetCompositingQuality = gdiplus.NewProc("GdipSetCompositingQuality")

	pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	pTaskDialogIndirect   = comctl32.NewProc("TaskDialogIndirect")

	pSetWindowTheme        = uxtheme.NewProc("SetWindowTheme")
	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")

	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	pSHGetFileInfoW     = shell32.NewProc("SHGetFileInfoW")
	pDragAcceptFiles    = shell32.NewProc("DragAcceptFiles")
	pDragQueryFileW     = shell32.NewProc("DragQueryFileW")
	pDragFinish         = shell32.NewProc("DragFinish")
	pSHObjectProperties = shell32.NewProc("SHObjectProperties")
)

const (
	wmCreate        = 0x0001
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmSetFocus      = 0x0007
	wmPaint         = 0x000F
	wmClose         = 0x0010
	wmEraseBkgnd    = 0x0014
	wmSetCursor     = 0x0020
	wmGetMinMaxInfo = 0x0024
	wmSetFont       = 0x0030
	wmCopyData      = 0x004A
	wmNotify        = 0x004E
	wmContextMenu   = 0x007B
	wmKeyDown       = 0x0100
	wmChar          = 0x0102
	wmSysKeyDown    = 0x0104
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmCtlColorEdit  = 0x0133
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonDown   = 0x0204
	wmRButtonUp     = 0x0205
	wmMouseWheel    = 0x020A
	wmXButtonUp     = 0x020C
	wmDropFiles     = 0x0233
	wmMouseLeave    = 0x02A3
	wmDpiChanged    = 0x02E0
	wmApp           = 0x8000

	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsClipChildren     = 0x02000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000

	vkBack   = 0x08
	vkTab    = 0x09
	vkReturn = 0x0D
	vkShift  = 0x10
	vkCtrl   = 0x11
	vkMenu   = 0x12
	vkEscape = 0x1B
	vkLeft   = 0x25
	vkUp     = 0x26
	vkDown   = 0x28
	vkRight  = 0x27
	vkDelete = 0x2E
	vkF5     = 0x74

	swShow = 5

	srcCopy = 0x00CC0020
)

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) W() int32 { return r.Right - r.Left }
func (r rect) H() int32 { return r.Bottom - r.Top }
func (r rect) has(x, y int32) bool {
	return x >= r.Left && x < r.Right && y >= r.Top && y < r.Bottom
}
func mkRect(x, y, w, h int32) rect { return rect{x, y, x + w, y + h} }

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type nmhdr struct {
	HwndFrom uintptr
	IDFrom   uintptr
	Code     uint32
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	HwndTrack uintptr
	HoverTime uint32
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type triVertex struct {
	X, Y                    int32
	Red, Green, Blue, Alpha uint16
}

func lo16(v uintptr) int32 { return int32(int16(v & 0xFFFF)) }
func hi16(v uintptr) int32 { return int32(int16((v >> 16) & 0xFFFF)) }

func u16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func sendMessage(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := pSendMessageW.Call(hwnd, uintptr(m), w, l)
	return r
}

func postMessage(hwnd uintptr, m uint32, w, l uintptr) {
	pPostMessageW.Call(hwnd, uintptr(m), w, l)
}

func invalidate(hwnd uintptr, r *rect) {
	pInvalidateRect.Call(hwnd, uintptr(unsafe.Pointer(r)), 0)
}

func clientRect(hwnd uintptr) rect {
	var r rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func keyDown(vk int) bool {
	r, _, _ := pGetKeyState.Call(uintptr(vk))
	return int16(r) < 0
}

func moduleHandle() uintptr {
	h, _, _ := pGetModuleHandleW.Call(0)
	return h
}

func windowText(hwnd uintptr) string {
	n, _, _ := pGetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}

func setWindowText(hwnd uintptr, s string) {
	pSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(u16(s))))
}

// setClipboardText 把文字放进剪贴板
func setClipboardText(hwnd uintptr, s string) bool {
	text, err := windows.UTF16FromString(s)
	if err != nil {
		return false
	}
	if r, _, _ := pOpenClipboard.Call(hwnd); r == 0 {
		return false
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	const gmemMoveable, cfUnicodeText = 0x0002, 13
	size := uintptr(len(text) * 2)
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return false
	}
	p, _, _ := pGlobalLock.Call(h)
	copy(unsafe.Slice((*uint16)(ptr(p)), len(text)), text)
	pGlobalUnlock.Call(h)
	r, _, _ := pSetClipboardData.Call(cfUnicodeText, h)
	return r != 0
}

var _ = syscall.SyscallN

// ptr 把消息参数里的指针还原出来（系统给的内存，不归 Go 管）
func ptr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }
