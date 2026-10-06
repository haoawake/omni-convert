//go:build windows || darwin

package pdf

// pdfium 的绑定：不用 cgo，直接调动态库的导出函数（Windows 上是 pdfium.dll，用 syscall；
// macOS 上是 libpdfium.dylib，用 purego）。怎么加载见 pdfium_load_*.go。
// pdfium 不是线程安全的，所有调用都要先拿 pdfiumMu。
// 这种调用方式读不了 float/double 返回值，所以只用返回整数、指针的函数，尺寸用输出参数取。

import (
	"image"
	"io"
	"os"
	"runtime"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

var (
	pdfiumMu   sync.Mutex
	pdfiumOnce sync.Once
	pdfiumErr  error
	fp         pdfiumProcs
)

type pdfiumProcs struct {
	initLibrary,
	loadDocument,
	loadMemDocument64,
	closeDocument,
	getPageCount,
	getPageSizeByIndex,
	loadPage,
	closePage,
	bitmapCreateEx,
	bitmapFillRect,
	bitmapDestroy,
	renderPageBitmap,
	textLoadPage,
	textClosePage,
	textCountChars,
	textGetText,
	createNewDocument,
	importPagesByIndex,
	saveAsCopy,
	getSecurityHandlerRevision proc
}

// pdfium 的错误码（FPDF_GetLastError 的值）
const (
	errUnknown  = 1
	errFile     = 2
	errFormat   = 3
	errPassword = 4
	errSecurity = 5
)

// 渲染标志：画出批注（表单、印章、手写批注等）
const renderAnnot = 0x01

// 保存标志
const (
	saveNoIncremental  = 2
	saveRemoveSecurity = 3
)

// pdfiumAvailable 检查 pdfium.dll 能不能用
func pdfiumAvailable() error {
	pdfiumOnce.Do(loadPdfium)
	return pdfiumErr
}

func loadPdfium() {
	path := tools.PdfiumDLL()
	if err := tools.Need(path); err != nil {
		pdfiumErr = err
		return
	}
	procs := map[string]*proc{
		"FPDF_InitLibrary":                &fp.initLibrary,
		"FPDF_LoadDocument":               &fp.loadDocument,
		"FPDF_LoadMemDocument64":          &fp.loadMemDocument64,
		"FPDF_CloseDocument":              &fp.closeDocument,
		"FPDF_GetPageCount":               &fp.getPageCount,
		"FPDF_GetPageSizeByIndex":         &fp.getPageSizeByIndex,
		"FPDF_LoadPage":                   &fp.loadPage,
		"FPDF_ClosePage":                  &fp.closePage,
		"FPDFBitmap_CreateEx":             &fp.bitmapCreateEx,
		"FPDFBitmap_FillRect":             &fp.bitmapFillRect,
		"FPDFBitmap_Destroy":              &fp.bitmapDestroy,
		"FPDF_RenderPageBitmap":           &fp.renderPageBitmap,
		"FPDFText_LoadPage":               &fp.textLoadPage,
		"FPDFText_ClosePage":              &fp.textClosePage,
		"FPDFText_CountChars":             &fp.textCountChars,
		"FPDFText_GetText":                &fp.textGetText,
		"FPDF_CreateNewDocument":          &fp.createNewDocument,
		"FPDF_ImportPagesByIndex":         &fp.importPagesByIndex,
		"FPDF_SaveAsCopy":                 &fp.saveAsCopy,
		"FPDF_GetSecurityHandlerRevision": &fp.getSecurityHandlerRevision,
	}
	if pdfiumErr = loadProcs(path, procs); pdfiumErr != nil {
		return
	}
	pdfiumMu.Lock()
	fp.initLibrary.Call()
	pdfiumMu.Unlock()
}

// cstr 把 Go 字符串变成以 0 结尾的 UTF-8 字节串
func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func ptr(b []byte) uintptr {
	if len(b) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&b[0]))
}

// rawPtr 把 C 那边给的地址变回指针（这样写 go vet 不会误报）
func rawPtr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// Doc 是用 pdfium 打开的一个 PDF。方法内部自己加锁，可以在不同 goroutine 里用，但同一时间只有一个在干活。
type Doc struct {
	h     uintptr
	data  []byte // 用内存方式打开时，数据要一直留到关闭
	pages int
	path  string
}

// Open 用 pdfium 打开 PDF。password 可以为空。错误信息是给用户看的中文。
func Open(path, password string) (*Doc, error) {
	if err := pdfiumAvailable(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, conv.Fail("找不到这个文件", path)
	}
	d, err := openOnce(path, password)
	if err == errWrongPassword {
		// 全角字符、全角空格之类：按 PDF 标准（SASLprep）规范化之后再试一次
		if alt := normPassword(password); alt != password {
			if d2, err2 := openOnce(path, alt); err2 == nil {
				return d2, nil
			}
		}
	}
	return d, err
}

func openOnce(path, password string) (*Doc, error) {
	pw := cstr(password)
	p := cstr(path)

	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	h, _, e := fp.loadDocument.Call(ptr(p), ptr(pw))
	runtime.KeepAlive(p)
	var data []byte
	if h == 0 {
		code := lastErrno(e)
		if code != errFile {
			return nil, openError(code, password)
		}
		// 路径打不开（比如文件被别的程序以特殊方式占用），改成读进内存再打开
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, conv.Fail("打不开这个文件，可能正被别的程序占用", err.Error())
		}
		if len(data) == 0 {
			return nil, conv.Fail("这个 PDF 是空文件", "")
		}
		h, _, e = fp.loadMemDocument64.Call(ptr(data), uintptr(len(data)), ptr(pw))
		if h == 0 {
			return nil, openError(lastErrno(e), password)
		}
	}
	runtime.KeepAlive(pw)
	n, _, _ := fp.getPageCount.Call(h)
	d := &Doc{h: h, data: data, pages: int(int32(n)), path: path}
	if d.pages <= 0 {
		fp.closeDocument.Call(h)
		return nil, conv.Fail("这个 PDF 里没有页面", "")
	}
	return d, nil
}

func openError(code int, password string) error {
	switch code {
	case errPassword:
		if password == "" {
			return errNeedPassword
		}
		return errWrongPassword
	case errFile:
		return conv.Fail("打不开这个文件，可能正被别的程序占用", "")
	case errSecurity:
		return conv.Fail("这个 PDF 用了不支持的加密方式，打不开", "")
	case errFormat:
		return errCorrupt
	}
	return conv.Fail("PDF 文件已损坏，打不开", "pdfium 错误码 "+itoa(code))
}

// Close 关闭文档。可以重复调用。
func (d *Doc) Close() {
	if d == nil || d.h == 0 {
		return
	}
	pdfiumMu.Lock()
	fp.closeDocument.Call(d.h)
	pdfiumMu.Unlock()
	d.h = 0
	d.data = nil
}

// PageCount 是总页数
func (d *Doc) PageCount() int { return d.pages }

// Encrypted 说明这个文件有没有加密（包括只限制了编辑、打印的）
func (d *Doc) Encrypted() bool {
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	r, _, _ := fp.getSecurityHandlerRevision.Call(d.h)
	return int32(r) > 0
}

// PageSize 返回第 i 页（从 0 开始）显示出来的宽高，单位是点（1/72 英寸），已经考虑了页面旋转
func (d *Doc) PageSize(i int) (wPt, hPt float64) {
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	return d.pageSize(i)
}

func (d *Doc) pageSize(i int) (float64, float64) {
	var w, h float64
	r, _, _ := fp.getPageSizeByIndex.Call(d.h, uintptr(i), uintptr(unsafe.Pointer(&w)), uintptr(unsafe.Pointer(&h)))
	if int32(r) == 0 || !(w > 0) || !(h > 0) {
		return 612, 792 // 读不到时按 Letter 算
	}
	return w, h
}

// Render 按 dpi 把第 i 页（从 0 开始）画成图片，白底
func (d *Doc) Render(i int, dpi float64) (*image.RGBA, error) {
	w, h := d.PageSize(i)
	pw, ph := pixelSize(w, h, dpi/72)
	return d.RenderPx(i, pw, ph)
}

// RenderPx 把第 i 页画成正好 w×h 像素的图片（尺寸会被限制在安全范围内）
func (d *Doc) RenderPx(i, w, h int) (*image.RGBA, error) {
	if i < 0 || i >= d.pages {
		return nil, conv.Fail("页码超出范围", "")
	}
	w, h = clampPixels(w, h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 白底（pdfium 也会填，这里先填好以防万一）
	for k := range img.Pix {
		img.Pix[k] = 0xff
	}

	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	page, _, _ := fp.loadPage.Call(d.h, uintptr(i))
	if page == 0 {
		return nil, conv.Fail("第 "+itoa(i+1)+" 页已损坏，读不出来", "")
	}
	defer fp.closePage.Call(page)

	var pin runtime.Pinner
	pin.Pin(&img.Pix[0])
	defer pin.Unpin()
	const formatBGRA = 4
	bmp, _, _ := fp.bitmapCreateEx.Call(uintptr(w), uintptr(h), formatBGRA, uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(img.Stride))
	if bmp == 0 {
		return nil, conv.Fail("内存不够，画不出这一页", itoa(w)+"×"+itoa(h))
	}
	defer fp.bitmapDestroy.Call(bmp)
	fp.bitmapFillRect.Call(bmp, 0, 0, uintptr(w), uintptr(h), 0xFFFFFFFF)
	fp.renderPageBitmap.Call(bmp, page, 0, 0, uintptr(w), uintptr(h), 0, renderAnnot)
	// BGRA → RGBA，同时把透明度设成不透明
	pix := img.Pix
	for k := 0; k+3 < len(pix); k += 4 {
		pix[k], pix[k+2], pix[k+3] = pix[k+2], pix[k], 0xff
	}
	return img, nil
}

// Text 取出第 i 页（从 0 开始）的文字，行与行之间用 \n 分开
func (d *Doc) Text(i int) string {
	if i < 0 || i >= d.pages {
		return ""
	}
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	page, _, _ := fp.loadPage.Call(d.h, uintptr(i))
	if page == 0 {
		return ""
	}
	defer fp.closePage.Call(page)
	tp, _, _ := fp.textLoadPage.Call(page)
	if tp == 0 {
		return ""
	}
	defer fp.textClosePage.Call(tp)
	r, _, _ := fp.textCountChars.Call(tp)
	n := int(int32(r))
	if n <= 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	r, _, _ = fp.textGetText.Call(tp, 0, uintptr(n), uintptr(unsafe.Pointer(&buf[0])))
	got := int(int32(r))
	if got <= 0 {
		return ""
	}
	if got > len(buf) {
		got = len(buf)
	}
	return cleanText(string(utf16.Decode(buf[:got])))
}

// SaveCopy 把文档另存为 out，去掉密码和权限限制（顺便把结构理顺，常用来修复 pdfcpu 读不了的文件）
func (d *Doc) SaveCopy(out string) error {
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	return saveDoc(d.h, out, saveRemoveSecurity)
}

// importPages 新建一个 PDF，依次把每个文档里的指定页复制进去（pages[k] 为 nil 表示全部页）
func importPages(out string, docs []*Doc, pages [][]int) error {
	if err := pdfiumAvailable(); err != nil {
		return err
	}
	pdfiumMu.Lock()
	defer pdfiumMu.Unlock()
	dst, _, _ := fp.createNewDocument.Call()
	if dst == 0 {
		return conv.Fail("内存不够，没法新建 PDF", "")
	}
	defer fp.closeDocument.Call(dst)
	at := 0
	for k, d := range docs {
		idx := pages[k]
		if idx == nil {
			idx = make([]int, d.pages)
			for i := range idx {
				idx[i] = i
			}
		}
		if len(idx) == 0 {
			continue
		}
		c := make([]int32, len(idx))
		for i, p := range idx {
			c[i] = int32(p)
		}
		r, _, _ := fp.importPagesByIndex.Call(dst, d.h, uintptr(unsafe.Pointer(&c[0])), uintptr(len(c)), uintptr(at))
		runtime.KeepAlive(c)
		if int32(r) == 0 {
			return conv.Fail("复制页面失败", d.path)
		}
		at += len(idx)
	}
	return saveDoc(dst, out, saveNoIncremental)
}

// ---------------------------------------------------------------- 保存

// fileWrite 对应 C 里的 FPDF_FILEWRITE：{ int version; WriteBlock 函数指针 }
type fileWrite struct {
	version    int32
	_          int32
	writeBlock uintptr
}

var (
	writeCbOnce sync.Once
	writeCb     uintptr
	curWriter   io.Writer // 正在保存的目标，只在拿着 pdfiumMu 时使用
	curWriteErr error
)

func writeBlock(_ uintptr, data uintptr, size uintptr) uintptr {
	n := blockSize(size)
	if curWriter == nil || curWriteErr != nil {
		return 0
	}
	if n == 0 {
		return 1
	}
	b := unsafe.Slice((*byte)(rawPtr(data)), n)
	if _, err := curWriter.Write(b); err != nil {
		curWriteErr = err
		return 0
	}
	return 1
}

// saveDoc 把文档 h 写到 out。调用前必须已经拿着 pdfiumMu。
func saveDoc(h uintptr, out string, flags uintptr) error {
	writeCbOnce.Do(func() { writeCb = newCallback(writeBlock) })
	f, err := os.Create(out)
	if err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	bw := newBufWriter(f)
	fw := &fileWrite{version: 1, writeBlock: writeCb}
	curWriter, curWriteErr = bw, nil
	r, _, _ := fp.saveAsCopy.Call(h, uintptr(unsafe.Pointer(fw)), flags)
	runtime.KeepAlive(fw)
	werr := curWriteErr
	curWriter, curWriteErr = nil, nil
	if werr == nil {
		werr = bw.Flush()
	}
	cerr := f.Close()
	if int32(r) == 0 || werr != nil || cerr != nil {
		os.Remove(out)
		detail := ""
		if werr != nil {
			detail = werr.Error()
		} else if cerr != nil {
			detail = cerr.Error()
		}
		return conv.Fail("保存 PDF 失败", detail)
	}
	return nil
}
