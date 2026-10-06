//go:build !windows && !darwin

package pdf

// 其它平台没有 pdfium：需要渲染、取文字的功能一律报错，其余（合并、拆分、加密……）照常可用。

import (
	"image"

	"github.com/haoawake/omni-convert/internal/conv"
)

var errNoPdfium = conv.Fail("这个功能只支持 Windows 和 macOS", "")

func pdfiumAvailable() error { return errNoPdfium }

// Doc 是用 pdfium 打开的一个 PDF（这个平台上打不开）
type Doc struct{ pages int }

func Open(path, password string) (*Doc, error)                 { return nil, errNoPdfium }
func (d *Doc) Close()                                          {}
func (d *Doc) PageCount() int                                  { return d.pages }
func (d *Doc) Encrypted() bool                                 { return false }
func (d *Doc) PageSize(i int) (wPt, hPt float64)               { return 612, 792 }
func (d *Doc) Render(i int, dpi float64) (*image.RGBA, error)  { return nil, errNoPdfium }
func (d *Doc) RenderPx(i, w, h int) (*image.RGBA, error)       { return nil, errNoPdfium }
func (d *Doc) Text(i int) string                               { return "" }
func (d *Doc) SaveCopy(out string) error                       { return errNoPdfium }
func importPages(out string, docs []*Doc, pages [][]int) error { return errNoPdfium }
