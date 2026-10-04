//go:build windows

package office

import (
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 进程和窗口相关的小工具：找出我们自己启动的 Office 进程、卡死时结束它、检查它有没有弹对话框。

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procEnumChildWindows         = user32.NewProc("EnumChildWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procPeekMessageW             = user32.NewProc("PeekMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
)

// processes 列出某个程序（比如 WINWORD.EXE）当前所有的进程号
func processes(exe string) map[uint32]bool {
	out := map[uint32]bool{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exe) {
			out[e.ProcessID] = true
		}
	}
	return out
}

// pidOfWindow 返回窗口所属的进程号
func pidOfWindow(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// killPID 强制结束进程
func killPID(pid uint32) {
	if pid == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	windows.TerminateProcess(h, 1)
	windows.WaitForSingleObject(h, 5000)
}

// waitExit 等进程自己退出，超时返回 false。进程不存在也算已经退出。
func waitExit(pid uint32, d time.Duration) bool {
	if pid == 0 {
		return true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	return ev == windows.WAIT_OBJECT_0
}

// processAlive 判断进程还在不在
func processAlive(pid uint32) bool { return pid != 0 && !waitExit(pid, 0) }

func windowClass(h uintptr) string {
	var b [256]uint16
	n, _, _ := procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return windows.UTF16ToString(b[:n])
}

func windowText(h uintptr) string {
	var b [1024]uint16
	n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return windows.UTF16ToString(b[:n])
}

// 枚举窗口的回调只能创建有限个，所以全局只建一次，用 lParam 传一个编号找到对应的状态
type enumState struct {
	pid  uint32
	wins []uintptr
}

var (
	enumMu     sync.Mutex
	enumStates = map[uintptr]*enumState{}
	enumNext   uintptr
)

var enumCB = syscall.NewCallback(func(h, id uintptr) uintptr {
	enumMu.Lock()
	st := enumStates[id]
	enumMu.Unlock()
	if st != nil && (st.pid == 0 || pidOfWindow(h) == st.pid) {
		st.wins = append(st.wins, h)
	}
	return 1
})

// enumWindows 列出顶层窗口（parent 为 0）或某个窗口的子窗口，pid 不为 0 时只要这个进程的
func enumWindows(parent uintptr, pid uint32) []uintptr {
	st := &enumState{pid: pid}
	enumMu.Lock()
	enumNext++
	id := enumNext
	enumStates[id] = st
	enumMu.Unlock()
	if parent == 0 {
		procEnumWindows.Call(enumCB, id)
	} else {
		procEnumChildWindows.Call(parent, enumCB, id)
	}
	enumMu.Lock()
	delete(enumStates, id)
	enumMu.Unlock()
	return st.wins
}

// dialogsOf 找出进程 pid 正在显示的对话框，返回对话框里的文字。
// Office 的提示框是 #32770（标准对话框）、bosa_sdm_*（Word 老式对话框）或 NUIDialog（新式对话框）。
// 带进度条的（正在发布、正在保存）不算。
func dialogsOf(pid uint32) []string {
	if pid == 0 {
		return nil
	}
	var out []string
	for _, h := range enumWindows(0, pid) {
		if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
			continue
		}
		cls := windowClass(h)
		if cls != "#32770" && !strings.HasPrefix(cls, "bosa_sdm_") && cls != "NUIDialog" {
			continue
		}
		texts := []string{}
		progress := false
		for _, k := range enumWindows(h, 0) {
			kc := windowClass(k)
			if strings.Contains(strings.ToLower(kc), "progress") {
				progress = true
				break
			}
			if kc == "Static" || kc == "MSOUNISTAT" || kc == "RichEdit20W" {
				if t := strings.TrimSpace(windowText(k)); t != "" {
					texts = append(texts, t)
				}
			}
		}
		if progress {
			continue
		}
		text := strings.Join(texts, " ")
		if text == "" {
			text = windowText(h)
		}
		out = append(out, text)
	}
	return out
}

// pumpMessages 处理当前线程的窗口消息（COM 单线程套间要求有消息循环）
func pumpMessages() {
	var msg [48]byte // MSG 结构，64 位下 48 字节
	for {
		r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 1 /* PM_REMOVE */)
		if r == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
}
