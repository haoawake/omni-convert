// Package pdf 处理 PDF：转图片、转长图、提取文字、转 PPT、转 Word（纯文字）、合并、拆分、压缩、
// 加密、解密、旋转，以及把图片合成 PDF。
//
// 渲染和取文字用 pdfium.dll（只在 Windows 上有，见 pdfium_windows.go）；合并、拆分、加密、旋转、
// 压缩用纯 Go 的 pdfcpu。pdfcpu 读不了的「不太规范」的文件，会先用 pdfium 另存一份再交给 pdfcpu。
package pdf

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/haoawake/omni-convert/internal/conv"
)

var (
	errNeedPassword  = conv.Fail("这个 PDF 有打开密码，请在「密码」里填上", "")
	errWrongPassword = conv.Fail("密码不对，打不开这个 PDF", "")
	errCorrupt       = conv.Fail("PDF 文件已损坏，打不开", "")
)

// 渲染尺寸的上限，防止内存爆掉
const (
	maxSide   = 20000      // 单边最多多少像素
	maxPixels = 60_000_000 // 一张图最多多少像素（约 240 MB 内存）
)

func itoa(i int) string { return strconv.Itoa(i) }

// pixelSize 把以点为单位的尺寸按 scale（像素/点）换算成像素，至少 1
func pixelSize(wPt, hPt, scale float64) (int, int) {
	w := int(math.Round(wPt * scale))
	h := int(math.Round(hPt * scale))
	return max(w, 1), max(h, 1)
}

// clampPixels 按比例缩小过大的尺寸
func clampPixels(w, h int) (int, int) {
	w, h = max(w, 1), max(h, 1)
	s := 1.0
	if l := max(w, h); l > maxSide {
		s = float64(maxSide) / float64(l)
	}
	if p := float64(w) * float64(h) * s * s; p > maxPixels {
		s *= math.Sqrt(maxPixels / p)
	}
	if s < 1 {
		w = max(int(float64(w)*s), 1)
		h = max(int(float64(h)*s), 1)
	}
	return w, h
}

// cleanText 整理 pdfium 取出来的文字：统一换行、去掉控制字符和行尾空格
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == 0x02: // pdfium 用它表示行尾的连字符
			return '-'
		case r == 0xFFFE || r == 0xFFFF || r == 0xFFFD:
			return -1
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// cancelled 检查用户是否点了停止
func cancelled(ctx context.Context) error {
	if ctx.Err() != nil {
		return conv.ErrCancelled
	}
	return nil
}

// selectedPages 按「页码范围」选项算出要处理的页（从 0 开始）
func selectedPages(opt conv.Options, n int) ([]int, error) {
	rs, err := conv.ParseRanges(opt.Str(conv.OptPages, ""), n)
	if err != nil {
		return nil, err
	}
	ps := conv.Pages(rs)
	for i := range ps {
		ps[i]--
	}
	return ps, nil
}

// numWidth 是给 n 个文件编号时需要几位数（至少 3 位：001、002……）
func numWidth(n int) int { return max(3, len(strconv.Itoa(n))) }

func pad(i, width int) string {
	s := strconv.Itoa(i)
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

// writeFile 创建 path 并调用 write 写入内容；失败时删掉写了一半的文件。
// write 返回的错误原样传回（交给调用方翻译），文件本身写不进去时返回中文错误。
func writeFile(path string, write func(w io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	bw := newBufWriter(f)
	err = write(bw)
	if err == nil {
		if ferr := bw.Flush(); ferr != nil {
			err = conv.Fail("写入输出文件失败，磁盘可能满了", ferr.Error())
		}
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = conv.Fail("写入输出文件失败", cerr.Error())
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// normPassword 按 PDF 2.0 的要求（SASLprep）规范化密码：全角字母数字变半角、全角空格变普通空格
func normPassword(pw string) string { return norm.NFKC.String(pw) }

// safe 执行 f，把 panic 变成普通错误（第三方库遇到怪文件时可能 panic，不能让整个程序崩掉）
func safe(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}

// guard 是每个转换的最外层：panic 变成错误、取消统一返回 ErrCancelled、没翻译的错误统一给一句中文
func guard(ctx context.Context, f func() error) error {
	err := safe(f)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return conv.ErrCancelled
	case isUserErr(err):
		return err
	}
	return conv.Fail("处理这个 PDF 时出错了", err.Error())
}

// guarded 给 RunFunc 套上 guard
func guarded(f conv.RunFunc) conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		return guard(ctx, func() error { return f(ctx, j) })
	}
}

func newBufWriter(w io.Writer) *bufio.Writer { return bufio.NewWriterSize(w, 1<<20) }

// fileSize 返回文件大小，读不到时返回 0
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// copyFile 原样复制文件
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return conv.Fail("读不了这个文件", err.Error())
	}
	defer in.Close()
	return writeFile(dst, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

// isUserErr 判断是不是已经写好给用户看的错误（或者取消）
func isUserErr(err error) bool {
	var ue *conv.UserError
	return errors.As(err, &ue) || errors.Is(err, conv.ErrCancelled)
}

// PageCount 返回 PDF 的页数。password 可以为空。
func PageCount(path, password string) (int, error) {
	if pdfiumAvailable() == nil {
		d, err := Open(path, password)
		if err != nil {
			return 0, err
		}
		defer d.Close()
		return d.PageCount(), nil
	}
	return pdfcpuPageCount(path, password)
}
