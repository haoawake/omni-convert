package pdf

import (
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/haoawake/omni-convert/internal/conv"
)

// loadProcs 加载 pdfium.dll，找到要用的每一个导出函数
func loadProcs(path string, procs map[string]*proc) error {
	dll := windows.NewLazyDLL(path)
	if err := dll.Load(); err != nil {
		return conv.Fail("PDF 组件 pdfium 加载失败", err.Error())
	}
	for name, p := range procs {
		lp := dll.NewProc(name)
		if err := lp.Find(); err != nil {
			return conv.Fail("PDF 组件 pdfium 版本不对", name)
		}
		*p = lp
	}
	return nil
}

// lastErrno 取出 syscall 返回的 Windows 错误码（pdfium 在 Windows 上用 SetLastError 记录错误，
// FPDF_GetLastError 其实就是 GetLastError）
func lastErrno(e error) int {
	if en, ok := e.(syscall.Errno); ok {
		return int(en)
	}
	return errUnknown
}

// blockSize C 的 unsigned long 在 Windows 上是 32 位
func blockSize(size uintptr) int { return int(uint32(size)) }

func newCallback(fn any) uintptr { return windows.NewCallback(fn) }
