//go:build windows

package main

// 和系统打交道的部分：选择文件 / 文件夹的对话框、打开和定位文件、单实例、右键菜单、剪贴板里的文件。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/haoawake/omni-convert/internal/conv"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	pSHChangeNotify      = shell32.NewProc("SHChangeNotify")
)

// mainWindow 是程序窗口的句柄，弹出的对话框挂在它下面
var mainWindow atomic.Uintptr

// ---------------------------------------------------------------- 打开、定位

func shellOpen(target string) error {
	return windows.ShellExecute(windows.Handle(mainWindow.Load()), u16("open"), u16(target), nil, nil, windows.SW_SHOWNORMAL)
}

// revealFile 打开资源管理器并选中这个文件
func revealFile(path string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}

// ---------------------------------------------------------------- COM 对话框

type comObj struct{ vtbl *[64]uintptr }

// call 调用 COM 方法。uintptrescapes 让传进来的指针参数（输出参数）留在堆上、调用期间不被挪动或回收。
//
//go:uintptrescapes
func (o *comObj) call(method int, args ...uintptr) uint32 {
	r, _, _ := syscall.SyscallN(o.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return uint32(r)
}

func (o *comObj) release() { o.call(2) }

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

// IFileDialog 的方法序号
const (
	fdShow            = 3
	fdSetFileTypes    = 4
	fdSetOptions      = 9
	fdGetOptions      = 10
	fdSetFolder       = 12
	fdSetTitle        = 17
	fdSetOkLabel      = 18
	fdGetResult       = 20
	fdGetResults      = 27
	siGetDisplayName  = 5
	siaGetCount       = 7
	siaGetItemAt      = 8
	fosPickFolders    = 0x20
	fosForceFS        = 0x40
	fosAllowMulti     = 0x200
	fosPathMustExist  = 0x800
	fosFileMustExist  = 0x1000
	sigdnFileSysPath  = 0x80058000
	hrCancelledByUser = 0x800704C7
)

func newFileDialog() (*comObj, error) {
	var dlg *comObj
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hr != 0 || dlg == nil {
		return nil, errors.New("没法打开选择文件的窗口")
	}
	return dlg, nil
}

func shellItemPath(item *comObj) string {
	var p *uint16
	if item.call(siGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))) != 0 || p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s
}

func setDialogFolder(dlg *comObj, dir string) {
	if dir == "" {
		return
	}
	fn := shell32.NewProc("SHCreateItemFromParsingName")
	iidShellItem := windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
	var item *comObj
	if hr, _, _ := fn.Call(uintptr(unsafe.Pointer(u16(dir))), 0, uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item))); hr == 0 && item != nil {
		dlg.call(fdSetFolder, uintptr(unsafe.Pointer(item)))
		item.release()
	}
}

// fileFilter 是对话框里的一种文件类型
type fileFilter struct{ name, spec string }

// pickFiles 弹出「打开」对话框，可以多选。用户取消返回 nil。
func pickFiles(title string, filters []fileFilter) []string {
	dlg, err := newFileDialog()
	if err != nil {
		showError(err.Error())
		return nil
	}
	defer dlg.release()
	var opts uint32
	dlg.call(fdGetOptions, uintptr(unsafe.Pointer(&opts)))
	dlg.call(fdSetOptions, uintptr(opts|fosAllowMulti|fosForceFS|fosFileMustExist))
	dlg.call(fdSetTitle, uintptr(unsafe.Pointer(u16(title))))
	type spec struct{ name, spec *uint16 }
	specs := make([]spec, len(filters))
	for i, f := range filters {
		specs[i] = spec{u16(f.name), u16(f.spec)}
	}
	if len(specs) > 0 {
		dlg.call(fdSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	}
	if dlg.call(fdShow, mainWindow.Load()) != 0 {
		return nil
	}
	var arr *comObj
	if dlg.call(fdGetResults, uintptr(unsafe.Pointer(&arr))) != 0 || arr == nil {
		return nil
	}
	defer arr.release()
	var n uint32
	arr.call(siaGetCount, uintptr(unsafe.Pointer(&n)))
	var out []string
	for i := uint32(0); i < n; i++ {
		var item *comObj
		if arr.call(siaGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item))) == 0 && item != nil {
			if p := shellItemPath(item); p != "" {
				out = append(out, p)
			}
			item.release()
		}
	}
	return out
}

// pickFolder 弹出「选择文件夹」对话框，用户取消返回空字符串
func pickFolder(title, start string) string {
	dlg, err := newFileDialog()
	if err != nil {
		showError(err.Error())
		return ""
	}
	defer dlg.release()
	var opts uint32
	dlg.call(fdGetOptions, uintptr(unsafe.Pointer(&opts)))
	dlg.call(fdSetOptions, uintptr(opts|fosPickFolders|fosForceFS|fosPathMustExist))
	dlg.call(fdSetTitle, uintptr(unsafe.Pointer(u16(title))))
	setDialogFolder(dlg, start)
	if dlg.call(fdShow, mainWindow.Load()) != 0 {
		return ""
	}
	var item *comObj
	if dlg.call(fdGetResult, uintptr(unsafe.Pointer(&item))) != 0 || item == nil {
		return ""
	}
	defer item.release()
	return shellItemPath(item)
}

// ---------------------------------------------------------------- 单实例

const (
	windowClass = "OmniConvertWindow"
	copyDataTag = 0x494E4D4F // "OMNI"
)

type copyDataStruct struct {
	Data uintptr
	Size uint32
	Ptr  uintptr
}

// forwardToRunning 如果已经有一个窗口开着，把文件交给它，返回 true（本进程直接退出）。
// 在资源管理器里选中一堆文件点右键菜单时，系统会为每个文件各启动一次本程序，
// 只有第一个真正打开窗口，其余的都把文件转交给它。
func forwardToRunning(files []string) bool {
	name := u16("Local\\OmniConvert-SingleInstance")
	h, err := windows.CreateMutex(nil, false, name)
	if err == nil {
		_ = h // 我们是第一个：互斥量一直留到进程退出
		return false
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return false
	}
	// 别的实例正在启动或已经开着：等它的窗口出现（最多 8 秒）
	find := user32.NewProc("FindWindowW")
	var hwnd uintptr
	for i := 0; i < 160; i++ {
		hwnd, _, _ = find.Call(uintptr(unsafe.Pointer(u16(windowClass))), 0)
		if hwnd != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hwnd == 0 {
		return false // 等不到就自己开一个
	}
	var pid uint32
	user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	user32.NewProc("AllowSetForegroundWindow").Call(uintptr(pid))
	var abs []string
	for _, f := range files {
		if p, err := filepath.Abs(f); err == nil {
			abs = append(abs, p)
		}
	}
	data, _ := windows.UTF16FromString(strings.Join(abs, "\x00"))
	cds := copyDataStruct{Data: copyDataTag, Size: uint32(len(data) * 2)}
	if len(data) > 0 {
		cds.Ptr = uintptr(unsafe.Pointer(&data[0]))
	}
	r, _, _ := user32.NewProc("SendMessageTimeoutW").Call(hwnd, wmCopyData, 0, uintptr(unsafe.Pointer(&cds)), 0x2 /* SMTO_ABORTIFHUNG */, 5000, 0)
	return r != 0
}

// readCopyData 取出别的实例发来的文件列表
func readCopyData(lp uintptr) ([]string, bool) {
	cds := (*copyDataStruct)(ptr(lp))
	if cds.Data != copyDataTag || cds.Ptr == 0 || cds.Size < 2 {
		return nil, cds.Data == copyDataTag
	}
	u := unsafe.Slice((*uint16)(ptr(cds.Ptr)), cds.Size/2)
	s := windows.UTF16ToString(u)
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, true
}

// bringToFront 把窗口拉到最前面（最小化了就还原）
func bringToFront(hwnd uintptr) {
	if r, _, _ := user32.NewProc("IsIconic").Call(hwnd); r != 0 {
		pShowWindow.Call(hwnd, 9 /* SW_RESTORE */)
	}
	pSetForegroundWindow.Call(hwnd)
}

// flashWindow 窗口不在前台时闪一下任务栏按钮，提醒转换完了
func flashWindow(hwnd uintptr) {
	if fg, _, _ := user32.NewProc("GetForegroundWindow").Call(); fg == hwnd {
		return
	}
	fi := struct {
		Size    uint32
		Hwnd    uintptr
		Flags   uint32
		Count   uint32
		Timeout uint32
	}{Hwnd: hwnd, Flags: 0x3 | 0xC /* FLASHW_ALL | FLASHW_TIMERNOFG */}
	fi.Size = uint32(unsafe.Sizeof(fi))
	user32.NewProc("FlashWindowEx").Call(uintptr(unsafe.Pointer(&fi)))
}

// ---------------------------------------------------------------- 右键菜单

const menuVerb = "OmniConvert"

// menuExts 是会在右键菜单里出现的文件类型
func menuExts() []string {
	var out []string
	for k := conv.Kind(0); k < conv.NumKinds; k++ {
		out = append(out, conv.ExtsOf(k)...)
	}
	sort.Strings(out)
	return out
}

func menuKeys() []string {
	keys := []string{`Software\Classes\Directory\shell\` + menuVerb}
	for _, e := range menuExts() {
		keys = append(keys, `Software\Classes\SystemFileAssociations\.`+e+`\shell\`+menuVerb)
	}
	return keys
}

// contextMenuInstalled 看右键菜单是不是已经加上了，并且指向的是现在这个程序
func contextMenuInstalled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\SystemFileAssociations\.jpg\shell\`+menuVerb+`\command`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil {
		return false
	}
	exe, _ := os.Executable()
	return strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// installContextMenu 在资源管理器的右键菜单里加上「用万能格式转换打开」（只对当前用户，不需要管理员）
func installContextMenu() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := `"` + exe + `" "%1"`
	for _, key := range menuKeys() {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
		if err != nil {
			return err
		}
		k.SetStringValue("", "用万能格式转换打开")
		k.SetStringValue("Icon", `"`+exe+`",0`)
		k.SetStringValue("MultiSelectModel", "Player")
		k.Close()
		c, _, err := registry.CreateKey(registry.CURRENT_USER, key+`\command`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		c.SetStringValue("", cmd)
		c.Close()
	}
	pSHChangeNotify.Call(0x08000000 /* SHCNE_ASSOCCHANGED */, 0, 0, 0)
	return nil
}

func uninstallContextMenu() error {
	var firstErr error
	for _, key := range menuKeys() {
		for _, sub := range []string{key + `\command`, key} {
			if err := registry.DeleteKey(registry.CURRENT_USER, sub); err != nil && !errors.Is(err, registry.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
	}
	pSHChangeNotify.Call(0x08000000, 0, 0, 0)
	return firstErr
}

// ---------------------------------------------------------------- 剪贴板

// clipboardFiles 取出剪贴板里复制的文件（在资源管理器里按 Ctrl+C 复制的那种）
func clipboardFiles(hwnd uintptr) []string {
	const cfHDrop = 15
	if r, _, _ := pOpenClipboard.Call(hwnd); r == 0 {
		return nil
	}
	defer pCloseClipboard.Call()
	h, _, _ := user32.NewProc("GetClipboardData").Call(cfHDrop)
	if h == 0 {
		return nil
	}
	return dropFiles(h)
}

// dropFiles 读出拖放 / 剪贴板句柄里的所有文件路径
func dropFiles(hdrop uintptr) []string {
	n, _, _ := pDragQueryFileW.Call(hdrop, 0xFFFFFFFF, 0, 0)
	var out []string
	buf := make([]uint16, 32768)
	for i := uintptr(0); i < n; i++ {
		l, _, _ := pDragQueryFileW.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if l > 0 {
			out = append(out, windows.UTF16ToString(buf[:l]))
		}
	}
	return out
}

// ---------------------------------------------------------------- 其他

func showError(msg string) {
	_, _ = windows.MessageBox(windows.HWND(mainWindow.Load()), u16(msg), u16(appName), windows.MB_ICONERROR|windows.MB_OK)
}

// settingsDir 是保存设置的文件夹
func settingsDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "OmniConvert")
}

// writable 判断能不能在这个文件夹里写文件（光盘、受保护的系统文件夹就不行）
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".omni-*.tmp")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// defaultOutDir 是原文件夹不能写时的去处：「文档\万能格式转换」
func defaultOutDir() string {
	home, _ := os.UserHomeDir()
	docs := filepath.Join(home, "Documents")
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil {
		docs = p
	}
	return filepath.Join(docs, appName)
}

// ---------------------------------------------------------------- 任务栏进度

var taskbar *comObj

// setTaskbarProgress 在任务栏按钮上显示进度：frac 0~1；frac < 0 时清除；failed 时显示成红色
func setTaskbarProgress(hwnd uintptr, frac float64, failed bool) {
	if taskbar == nil {
		clsid := windows.GUID{Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11D0, Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
		iid := windows.GUID{Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86, Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}
		var tb *comObj
		hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&tb)))
		if hr != 0 || tb == nil {
			return
		}
		tb.call(3) // HrInit
		taskbar = tb
	}
	const (
		setValue = 9
		setState = 10
	)
	switch {
	case frac < 0:
		taskbar.call(setState, hwnd, 0) // TBPF_NOPROGRESS
	default:
		state := uintptr(2) // TBPF_NORMAL
		if failed {
			state = 4 // TBPF_ERROR
		}
		taskbar.call(setState, hwnd, state)
		taskbar.call(setValue, hwnd, uintptr(min(max(frac, 0), 1)*1000), 1000)
	}
}
