package pdf

import (
	"github.com/ebitengine/purego"

	"github.com/haoawake/omni-convert/internal/conv"
)

// symbol 是用 dlsym 找到的一个导出函数
type symbol uintptr

// Call 调用这个函数。uintptrescapes：参数里由指针转成的 uintptr 指向的对象会留在堆上，
// 调用期间 Go 的栈扩容搬家也不会让 C 那边写到旧地址（比如 FPDF_GetPageSizeByIndex 的 &w、&h）
//
//go:uintptrescapes
func (s symbol) Call(a ...uintptr) (uintptr, uintptr, error) {
	r1, r2, _ := purego.SyscallN(uintptr(s), a...)
	return r1, r2, nil
}

// getLastError 是 FPDF_GetLastError（macOS 上 pdfium 自己记录错误码，不在 errno 里）
var getLastError symbol

// loadProcs 加载 libpdfium.dylib，找到要用的每一个导出函数
func loadProcs(path string, procs map[string]*proc) error {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return conv.Fail("PDF 组件 pdfium 加载失败", err.Error())
	}
	find := func(name string) (symbol, error) {
		addr, err := purego.Dlsym(lib, name)
		if err != nil || addr == 0 {
			return 0, conv.Fail("PDF 组件 pdfium 版本不对", name)
		}
		return symbol(addr), nil
	}
	for name, p := range procs {
		s, err := find(name)
		if err != nil {
			return err
		}
		*p = s
	}
	getLastError, err = find("FPDF_GetLastError")
	return err
}

// lastErrno 取出 pdfium 记下的错误码（调用时已经拿着 pdfiumMu）
func lastErrno(error) int {
	r, _, _ := getLastError.Call()
	if code := int(uint32(r)); code != 0 {
		return code
	}
	return errUnknown
}

// blockSize C 的 unsigned long 在 macOS 上是 64 位
func blockSize(size uintptr) int { return int(size) }

func newCallback(fn any) uintptr { return purego.NewCallback(fn) }
