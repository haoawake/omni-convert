package pdf

// PDF 转图片、转长图

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/haoawake/omni-convert/internal/conv"
)

// openForJob 用任务里的密码打开 src
func openForJob(j *conv.Job, src string) (*Doc, error) {
	return Open(src, j.Opt.Str(conv.OptPassword, ""))
}

// jobDPI 读清晰度选项，默认 150，限制在 30~1200
func jobDPI(opt conv.Options, def float64) float64 {
	dpi := opt.Float(conv.OptDPI, def)
	if dpi <= 0 {
		dpi = def
	}
	return math.Min(math.Max(dpi, 30), 1200)
}

func jobQuality(opt conv.Options, def int) int {
	q := opt.Int(conv.OptQuality, def)
	if q < 1 || q > 100 {
		q = def
	}
	return q
}

// ToImages 把 PDF 每页转成一张图片（format 是 "png" 或 "jpg"）。
// 选项：dpi（默认 150）、pages（默认全部）、password、quality（jpg 用，默认 90）。
// 多页时输出到「文件名」文件夹里的 文件名_001.png……；只选了一页时直接输出 文件名.png。
func ToImages(format string) conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		return imagesFrom(ctx, j, j.Input(), format)
	})
}

// ImagesFrom 同 ToImages，但渲染的是 src（比如 Word 先转出来的临时 PDF），输出文件按 j.Base() 命名
func ImagesFrom(ctx context.Context, j *conv.Job, src, format string) error {
	return guard(ctx, func() error { return imagesFrom(ctx, j, src, format) })
}

func imagesFrom(ctx context.Context, j *conv.Job, src, format string) error {
	format = strings.ToLower(strings.TrimPrefix(format, "."))
	ext := ".png"
	if format == "jpg" || format == "jpeg" {
		ext = ".jpg"
	}
	d, err := openForJob(j, src)
	if err != nil {
		return err
	}
	defer d.Close()
	pages, err := selectedPages(j.Opt, d.PageCount())
	if err != nil {
		return err
	}
	dpi := jobDPI(j.Opt, 150)
	q := jobQuality(j.Opt, 90)
	base := j.Base()

	var paths []string
	if len(pages) == 1 {
		paths = []string{j.OutFile(ext)}
	} else {
		dir, err := j.OutFolder(base)
		if err != nil {
			return conv.Fail("没法创建输出文件夹", err.Error())
		}
		w := numWidth(d.PageCount())
		for _, p := range pages {
			paths = append(paths, filepath.Join(dir, conv.SafeName(base+"_"+pad(p+1, w))+ext))
		}
	}

	render := func(_, p int) (*image.RGBA, error) { return d.Render(p, dpi) }
	encode := func(k int, img *image.RGBA) (any, error) {
		return nil, writeFile(paths[k], func(w io.Writer) error { return encodeImage(w, img, ext, q) })
	}
	maxPx := maxPagePixels(d, pages, dpi/72)
	return pipeline(ctx, j, pages, render, encode, nil, workersFor(maxPx))
}

// encodeImage 按扩展名把图片编码成 PNG 或 JPEG
func encodeImage(w io.Writer, img image.Image, ext string, quality int) error {
	if ext == ".jpg" {
		return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
	}
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	return enc.Encode(w, img)
}

// maxPagePixels 估计这些页里最大一页渲染后有多少像素
func maxPagePixels(d *Doc, pages []int, scale float64) int {
	m := 1
	for _, p := range pages {
		w, h := d.PageSize(p)
		pw, ph := clampPixels(pixelSize(w, h, scale))
		m = max(m, pw*ph)
	}
	return m
}

// workersFor 按单页大小决定同时编码几页（每页 4 字节/像素，总共控制在 600 MB 左右）
func workersFor(pagePixels int) int {
	n := min(runtime.NumCPU(), 4)
	byMem := 150_000_000 / max(pagePixels, 1)
	return max(1, min(n, byMem))
}

type pipeResult struct {
	v   any
	err error
}

// pipeline 依次渲染每一页（pdfium 只能一页一页来），渲染好的页交给 encode 并行处理，
// 再按页码顺序交给 emit（可以为 nil）。同时在处理中的页最多 workers 个。
func pipeline(ctx context.Context, j *conv.Job, pages []int,
	render func(k, page int) (*image.RGBA, error),
	encode func(k int, img *image.RGBA) (any, error),
	emit func(k int, v any) error, workers int) error {

	n := len(pages)
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]chan pipeResult, n)
	for k := range results {
		results[k] = make(chan pipeResult, 1)
	}
	sem := make(chan struct{}, max(workers, 1))
	var encWG sync.WaitGroup
	var dispatched atomic.Int64 // 已经有结果（或者正在编码）的页数
	prodDone := make(chan struct{})
	go func() {
		defer close(prodDone)
		for k, p := range pages {
			select {
			case sem <- struct{}{}:
			case <-c.Done():
				return
			}
			if c.Err() != nil {
				return
			}
			img, err := render(k, p)
			if err != nil {
				results[k] <- pipeResult{err: err}
				dispatched.Store(int64(k + 1))
				return
			}
			encWG.Add(1)
			go func(k int, img *image.RGBA) {
				defer encWG.Done()
				v, err := encode(k, img)
				results[k] <- pipeResult{v, err}
			}(k, img)
			dispatched.Store(int64(k + 1))
		}
	}()
	// 退出前一定要等渲染的 goroutine 停下来，之后调用方才能关闭文档
	defer func() {
		cancel()
		<-prodDone
		encWG.Wait()
	}()

	for k := range pages {
		j.Report(float64(k)/float64(n), fmt.Sprintf("第 %d/%d 页", k+1, n))
		var r pipeResult
		select {
		case r = <-results[k]:
		case <-ctx.Done():
			return conv.ErrCancelled
		case <-prodDone:
			// 渲染那边已经停了：这一页已经交出去的话等它的结果，否则就是被取消了
			if int64(k) >= dispatched.Load() {
				if ctx.Err() != nil {
					return conv.ErrCancelled
				}
				return conv.Fail("渲染中断", "")
			}
			r = <-results[k]
		}
		<-sem
		if r.err != nil {
			if ctx.Err() != nil {
				return conv.ErrCancelled
			}
			return r.err
		}
		if emit != nil {
			if err := emit(k, r.v); err != nil {
				return err
			}
		}
	}
	return cancelled(ctx)
}

// ---------------------------------------------------------------- 长图

const (
	jpegMaxSide   = 65535       // JPEG 的高度上限
	longMaxPixels = 120_000_000 // 每张长图最多多少像素（YCbCr 约 180 MB 内存）
)

// ToLongImage 把（选中的）所有页从上到下拼成一张长图 JPG。
// 选项：longw 宽度（默认 1080）、pages、password、quality（默认 90）。
// 太长（超过 JPEG 的 65535 像素上限）时拆成 文件名_长图1.jpg、文件名_长图2.jpg……尽量在两页之间切开。
func ToLongImage() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		return longImageFrom(ctx, j, j.Input())
	})
}

// longPiece 是长图里的一段：第 page 页从 y0 到 y1 行，贴到长图的 at 行
type longPiece struct {
	page, y0, y1, at int
}

type longPart struct {
	h      int
	pieces []longPiece
}

// LongImageFrom 同 ToLongImage，但渲染的是 src，输出文件按 j.Base() 命名
func LongImageFrom(ctx context.Context, j *conv.Job, src string) error {
	return guard(ctx, func() error { return longImageFrom(ctx, j, src) })
}

func longImageFrom(ctx context.Context, j *conv.Job, src string) error {
	d, err := openForJob(j, src)
	if err != nil {
		return err
	}
	defer d.Close()
	pages, err := selectedPages(j.Opt, d.PageCount())
	if err != nil {
		return err
	}
	W := j.Opt.Int(conv.OptLongW, 1080)
	if W <= 0 {
		W = 1080
	}
	W = min(max(W, 100), 8000)
	q := jobQuality(j.Opt, 90)
	gap := max(4, W/120) // 页与页之间的浅灰色间隔

	// 每页渲染成宽 W 的图片后有多高
	sizes := make([][2]int, len(pages))
	for k, p := range pages {
		w, h := d.PageSize(p)
		pw, ph := clampPixels(W, max(1, int(math.Round(float64(W)*h/w))))
		sizes[k] = [2]int{pw, ph}
	}
	limit := min(jpegMaxSide, longMaxPixels/W)
	parts := planLong(sizes, gap, limit)

	base := j.Base()
	var paths []string
	if len(parts) == 1 {
		paths = []string{j.OutFileNamed(base+"_长图", ".jpg")}
	} else {
		for i := range parts {
			paths = append(paths, j.OutFileNamed(fmt.Sprintf("%s_长图%d", base, i+1), ".jpg"))
		}
	}

	done, total := 0, len(pages)
	var cached *image.RGBA // 一页被切到两张长图里时，第二次不用重新渲染
	cachedK := -1
	for pi, part := range parts {
		canvas := image.NewYCbCr(image.Rect(0, 0, W, part.h), image.YCbCrSubsampleRatio420)
		fillYCbCr(canvas, 0, part.h, 0xE6, 0xE6, 0xE6)
		for _, pc := range part.pieces {
			if err := cancelled(ctx); err != nil {
				return err
			}
			if cachedK != pc.page {
				j.Report(float64(done)/float64(total+len(parts)), fmt.Sprintf("第 %d/%d 页", done+1, total))
				cached, err = d.RenderPx(pages[pc.page], sizes[pc.page][0], sizes[pc.page][1])
				if err != nil {
					return err
				}
				cachedK = pc.page
				done++
			}
			// 页面比长图窄（被限制了尺寸）时居中，两边留白
			fillYCbCr(canvas, pc.at, pc.at+pc.y1-pc.y0, 0xff, 0xff, 0xff)
			x0 := (W - cached.Bounds().Dx()) / 2
			copyToYCbCr(canvas, cached, x0, pc.at, pc.y0, pc.y1)
		}
		if err := cancelled(ctx); err != nil {
			return err
		}
		j.Report(float64(done+pi)/float64(total+len(parts)), "正在保存长图")
		if err := writeFile(paths[pi], func(w io.Writer) error {
			return jpeg.Encode(w, canvas, &jpeg.Options{Quality: q})
		}); err != nil {
			return err
		}
	}
	return nil
}

// planLong 把各页（高度 sizes[k][1]）排进一张张不超过 limit 高的长图，页与页之间隔 gap
func planLong(sizes [][2]int, gap, limit int) []longPart {
	var parts []longPart
	cur := longPart{}
	flush := func() {
		if len(cur.pieces) > 0 {
			parts = append(parts, cur)
		}
		cur = longPart{}
	}
	for k, s := range sizes {
		h := s[1]
		need := h
		if len(cur.pieces) > 0 {
			need += gap
		}
		if cur.h+need <= limit {
			if len(cur.pieces) > 0 {
				cur.h += gap
			}
			cur.pieces = append(cur.pieces, longPiece{k, 0, h, cur.h})
			cur.h += h
			continue
		}
		if h <= limit {
			// 放不下就从下一张开始，保证在两页之间切开
			flush()
			cur.pieces = append(cur.pieces, longPiece{k, 0, h, 0})
			cur.h = h
			continue
		}
		// 一页本身就比上限还长：只好从页面中间切开
		flush()
		for y := 0; y < h; y += limit {
			y1 := min(y+limit, h)
			cur.pieces = append(cur.pieces, longPiece{k, y, y1, 0})
			cur.h = y1 - y
			if y1 < h {
				flush()
			}
		}
	}
	flush()
	return parts
}

// fillYCbCr 把第 y0~y1 行填成一个颜色
func fillYCbCr(m *image.YCbCr, y0, y1 int, r, g, b uint8) {
	yy, cb, cr := rgbToYCbCr(r, g, b)
	w := m.Rect.Dx()
	for y := y0; y < y1; y++ {
		row := m.Y[y*m.YStride : y*m.YStride+w]
		for i := range row {
			row[i] = yy
		}
	}
	for y := y0 / 2; y < (y1+1)/2; y++ {
		o := y * m.CStride
		for x := 0; x < (w+1)/2; x++ {
			m.Cb[o+x] = cb
			m.Cr[o+x] = cr
		}
	}
}

// copyToYCbCr 把 src 的第 y0~y1 行贴到 dst 的 (x0, at) 处，色度按 2×2 取平均
func copyToYCbCr(dst *image.YCbCr, src *image.RGBA, x0, at, y0, y1 int) {
	sw := src.Bounds().Dx()
	dw := dst.Rect.Dx()
	sw = min(sw, dw-x0)
	h := y1 - y0
	for y := 0; y < h; y++ {
		srow := src.Pix[(y0+y)*src.Stride:]
		drow := dst.Y[(at+y)*dst.YStride:]
		for x := 0; x < sw; x++ {
			p := srow[x*4:]
			yy, _, _ := rgbToYCbCr(p[0], p[1], p[2])
			drow[x0+x] = yy
		}
	}
	// 色度：dst 里每个 2×2 块对应一个 Cb/Cr，只处理被这一段覆盖的块
	for cy := at / 2; cy <= (at+h-1)/2; cy++ {
		for cx := x0 / 2; cx <= (x0+sw-1)/2; cx++ {
			var sr, sg, sb, n int
			for dy := 0; dy < 2; dy++ {
				yy := cy*2 + dy - at + y0 // 对应 src 的行
				if yy < y0 || yy >= y1 {
					continue
				}
				for dx := 0; dx < 2; dx++ {
					xx := cx*2 + dx - x0
					if xx < 0 || xx >= sw {
						continue
					}
					p := src.Pix[yy*src.Stride+xx*4:]
					sr += int(p[0])
					sg += int(p[1])
					sb += int(p[2])
					n++
				}
			}
			if n == 0 {
				continue
			}
			_, cb, cr := rgbToYCbCr(uint8(sr/n), uint8(sg/n), uint8(sb/n))
			o := cy*dst.CStride + cx
			dst.Cb[o] = cb
			dst.Cr[o] = cr
		}
	}
}

// rgbToYCbCr 和 image/color 里的公式一样（JFIF 标准）
func rgbToYCbCr(r, g, b uint8) (uint8, uint8, uint8) {
	r1, g1, b1 := int32(r), int32(g), int32(b)
	yy := (19595*r1 + 38470*g1 + 7471*b1 + 1<<15) >> 16
	cb := -11056*r1 - 21712*g1 + 32768*b1 + 257<<15
	if uint32(cb)&0xff000000 == 0 {
		cb >>= 16
	} else {
		cb = ^(cb >> 31)
	}
	cr := 32768*r1 - 27440*g1 - 5328*b1 + 257<<15
	if uint32(cr)&0xff000000 == 0 {
		cr >>= 16
	} else {
		cr = ^(cr >> 31)
	}
	return uint8(yy), uint8(cb), uint8(cr)
}
