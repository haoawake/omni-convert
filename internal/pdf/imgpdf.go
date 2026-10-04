package pdf

// 一个只会写「每页一张图」的极简 PDF 写入器：图片合成 PDF、强力压缩都用它。
// JPEG 原样嵌入（DCTDecode，不重新编码）；PNG 解码后用 Flate + PNG 预测器压缩，透明通道放进 SMask。

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/haoawake/omni-convert/internal/conv"
)

// pdfImage 是一张准备写进 PDF 的图片
type pdfImage struct {
	w, h   int
	filter string // "DCTDecode" 或 "FlateDecode"
	cs     string // 颜色空间，比如 "/DeviceRGB"
	bpc    int
	extra  string // 其它字典项，比如 /Decode、/DecodeParms
	data   []byte
	smask  *pdfImage
	orient int // EXIF 方向 1~8，0 当作 1
}

// dispSize 是按 EXIF 方向摆正之后的宽高
func (im *pdfImage) dispSize() (int, int) {
	if im.orient >= 5 && im.orient <= 8 {
		return im.h, im.w
	}
	return im.w, im.h
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// imgPDF 边写边出，内存里只留当前这一页
type imgPDF struct {
	w       *countWriter
	offsets map[int]int64
	next    int
	pages   []int
	err     error
}

const (
	objCatalog = 1
	objPages   = 2
	objInfo    = 3
)

func newImgPDF(w io.Writer) *imgPDF {
	p := &imgPDF{w: &countWriter{w: w}, offsets: map[int]int64{}, next: 4}
	p.printf("%%PDF-1.4\n%%\xE2\xE3\xCF\xD3\n")
	return p
}

func (p *imgPDF) printf(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}

func (p *imgPDF) write(b []byte) {
	if p.err == nil {
		_, p.err = p.w.Write(b)
	}
}

func (p *imgPDF) alloc() int {
	n := p.next
	p.next++
	return n
}

func (p *imgPDF) beginObj(n int) {
	p.offsets[n] = p.w.n
	p.printf("%d 0 obj\n", n)
}

func (p *imgPDF) stream(n int, dict string, data []byte) {
	p.beginObj(n)
	p.printf("<<%s /Length %d>>\nstream\n", dict, len(data))
	p.write(data)
	p.printf("\nendstream\nendobj\n")
}

func (p *imgPDF) image(n int, im *pdfImage) {
	smask := ""
	var sn int
	if im.smask != nil {
		sn = p.alloc()
		smask = fmt.Sprintf(" /SMask %d 0 R", sn)
	}
	dict := fmt.Sprintf(" /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent %d /Filter /%s%s%s",
		im.w, im.h, im.cs, im.bpc, im.filter, im.extra, smask)
	p.stream(n, dict, im.data)
	if im.smask != nil {
		p.image(sn, im.smask)
	}
}

// addPage 加一页：页面 pw×ph 点，图片（摆正后）放在左下角 (x,y)、大小 dw×dh 点的位置
func (p *imgPDF) addPage(im *pdfImage, pw, ph, x, y, dw, dh float64) error {
	page, content, img := p.alloc(), p.alloc(), p.alloc()
	p.pages = append(p.pages, page)

	p.beginObj(page)
	p.printf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s] /Resources << /XObject << /Im0 %d 0 R >> /ProcSet [/PDF /ImageB /ImageC] >> /Contents %d 0 R >>\nendobj\n",
		objPages, num(pw), num(ph), img, content)

	a, b, c, d, e, f := orientMatrix(im.orient, x, y, dw, dh)
	cs := fmt.Sprintf("q %s %s %s %s %s %s cm /Im0 Do Q\n", num(a), num(b), num(c), num(d), num(e), num(f))
	p.stream(content, "", []byte(cs))
	p.image(img, im)
	return p.err
}

// orientMatrix 算出把图片（按 EXIF 方向摆正后）画到 (x,y,w,h) 的变换矩阵
func orientMatrix(o int, x, y, w, h float64) (a, b, c, d, e, f float64) {
	switch o {
	case 2: // 水平翻转
		return -w, 0, 0, h, x + w, y
	case 3: // 转 180°
		return -w, 0, 0, -h, x + w, y + h
	case 4: // 垂直翻转
		return w, 0, 0, -h, x, y + h
	case 5: // 沿左上—右下对角线翻转
		return 0, -h, -w, 0, x + w, y + h
	case 6: // 顺时针转 90°
		return 0, -h, w, 0, x, y + h
	case 7: // 沿右上—左下对角线翻转
		return 0, h, w, 0, x, y
	case 8: // 逆时针转 90°
		return 0, h, -w, 0, x + w, y
	}
	return w, 0, 0, h, x, y
}

// num 把数字写成 PDF 里的样子（最多 4 位小数，去掉多余的 0）
func num(v float64) string {
	if math.Abs(v) < 1e-6 {
		return "0"
	}
	s := fmt.Sprintf("%.4f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// pdfText 把字符串写成 PDF 文本字符串（UTF-16BE 十六进制）
func pdfText(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2+2*len(u))
	b[0], b[1] = 0xFE, 0xFF
	for i, c := range u {
		binary.BigEndian.PutUint16(b[2+2*i:], c)
	}
	return "<" + strings.ToUpper(hex.EncodeToString(b)) + ">"
}

// close 写目录、页面树、文档信息、交叉引用表和文件尾
func (p *imgPDF) close() error {
	if len(p.pages) == 0 {
		return conv.Fail("没有可以写入的页面", "")
	}
	p.beginObj(objCatalog)
	p.printf("<< /Type /Catalog /Pages %d 0 R >>\nendobj\n", objPages)

	var kids strings.Builder
	for i, k := range p.pages {
		if i > 0 {
			kids.WriteByte(' ')
		}
		fmt.Fprintf(&kids, "%d 0 R", k)
	}
	p.beginObj(objPages)
	p.printf("<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", kids.String(), len(p.pages))

	now := time.Now()
	_, off := now.Zone()
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	date := fmt.Sprintf("D:%s%c%02d'%02d'", now.Format("20060102150405"), sign, off/3600, off%3600/60)
	p.beginObj(objInfo)
	p.printf("<< /Producer %s /Creator %s /CreationDate (%s) /ModDate (%s) >>\nendobj\n",
		pdfText("万能格式转换"), pdfText("万能格式转换"), date, date)

	xref := p.w.n
	size := p.next
	p.printf("xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		off, ok := p.offsets[i]
		if !ok {
			p.printf("0000000000 65535 f \n")
			continue
		}
		p.printf("%010d 00000 n \n", off)
	}
	id := md5.Sum([]byte(fmt.Sprintf("%d %d %d", now.UnixNano(), len(p.pages), xref)))
	hid := strings.ToUpper(hex.EncodeToString(id[:]))
	p.printf("trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n",
		size, objCatalog, objInfo, hid, hid, xref)
	return p.err
}

// ---------------------------------------------------------------- 图片合成 PDF

// A4 纸，单位是点
const (
	a4W      = 595.28
	a4H      = 841.89
	a4Margin = 28.35 // 1 厘米
	fitMaxPt = 1190.0
)

// WriteImagesPDF 把图片（只支持 JPEG 和 PNG）合成一个 PDF，每张图一页。
// opt 里的 pagesize：fit（默认，页面跟图片一样大）| a4（A4 纸，图片居中，横图自动用横向 A4）。
func WriteImagesPDF(out string, images []string, opt conv.Options) error {
	if len(images) == 0 {
		return conv.Fail("没有图片", "")
	}
	a4 := strings.EqualFold(opt.Str(conv.OptPageSize, "fit"), "a4")
	return writeFile(out, func(w io.Writer) error {
		p := newImgPDF(w)
		for _, path := range images {
			im, err := loadPDFImage(path)
			if err != nil {
				return err
			}
			dw, dh := im.dispSize()
			var pw, ph, x, y, iw, ih float64
			if a4 {
				pw, ph = a4W, a4H
				if dw > dh {
					pw, ph = a4H, a4W
				}
				bw, bh := pw-2*a4Margin, ph-2*a4Margin
				s := math.Min(bw/float64(dw), bh/float64(dh))
				iw, ih = float64(dw)*s, float64(dh)*s
				x, y = (pw-iw)/2, (ph-ih)/2
			} else {
				// 按 96 dpi 换算成点；太大的缩到长边 1190 点（约 A3 的长边），太小的放大到至少 1 英寸
				s := 72.0 / 96
				if l := float64(max(dw, dh)) * s; l > fitMaxPt {
					s *= fitMaxPt / l
				} else if l < 72 {
					s *= 72 / l
				}
				iw, ih = float64(dw)*s, float64(dh)*s
				pw, ph = iw, ih
			}
			if err := p.addPage(im, pw, ph, x, y, iw, ih); err != nil {
				return err
			}
		}
		return p.close()
	})
}

// loadPDFImage 读一张 JPEG 或 PNG，准备好写进 PDF
func loadPDFImage(path string) (*pdfImage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, conv.Fail("读不了图片 "+filepath.Base(path), err.Error())
	}
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		im, err := jpegImage(data)
		if err != nil {
			return nil, conv.Fail("图片已损坏："+filepath.Base(path), err.Error())
		}
		return im, nil
	case len(data) > 8 && string(data[1:4]) == "PNG":
		m, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, conv.Fail("图片已损坏："+filepath.Base(path), err.Error())
		}
		return flateImage(m), nil
	}
	return nil, conv.Fail("只支持 JPG 和 PNG 图片："+filepath.Base(path), "")
}

// jpegInfo 是从 JPEG 文件头里读出来的信息
type jpegInfo struct {
	w, h, comps int
	adobe       bool // 有 Adobe APP14 标记（CMYK 时通常表示数据是反相的）
	orient      int
}

// parseJPEG 扫描 JPEG 的标记段，读出尺寸、通道数、EXIF 方向
func parseJPEG(b []byte) (jpegInfo, error) {
	var info jpegInfo
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return info, fmt.Errorf("不是 JPEG")
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			i++
			continue
		}
		m := b[i+1]
		if m == 0xFF {
			i++
			continue
		}
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			i += 2
			continue
		}
		if m == 0xD9 || m == 0xDA {
			break
		}
		l := int(binary.BigEndian.Uint16(b[i+2:]))
		if l < 2 || i+2+l > len(b) {
			return info, fmt.Errorf("标记段长度不对")
		}
		seg := b[i+4 : i+2+l]
		switch {
		case m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC:
			if len(seg) < 6 {
				return info, fmt.Errorf("SOF 太短")
			}
			if seg[0] != 8 {
				return info, fmt.Errorf("不支持 %d 位的 JPEG", seg[0])
			}
			info.h = int(binary.BigEndian.Uint16(seg[1:]))
			info.w = int(binary.BigEndian.Uint16(seg[3:]))
			info.comps = int(seg[5])
		case m == 0xEE && len(seg) >= 5 && string(seg[:5]) == "Adobe":
			info.adobe = true
		case m == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" && info.orient == 0:
			info.orient = exifOrientation(seg[6:])
		}
		i += 2 + l
	}
	if info.w <= 0 || info.h <= 0 || info.comps == 0 {
		return info, fmt.Errorf("读不到图片尺寸")
	}
	return info, nil
}

// exifOrientation 从 TIFF 结构里找方向标签（0x0112），找不到返回 0
func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			v := int(bo.Uint16(t[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}

// jpegImage 把 JPEG 原样包成 PDF 图片
func jpegImage(data []byte) (*pdfImage, error) {
	info, err := parseJPEG(data)
	if err != nil {
		return nil, err
	}
	im := &pdfImage{w: info.w, h: info.h, filter: "DCTDecode", bpc: 8, data: data, orient: info.orient}
	switch info.comps {
	case 1:
		im.cs = "/DeviceGray"
	case 3:
		im.cs = "/DeviceRGB"
	case 4:
		im.cs = "/DeviceCMYK"
		if info.adobe {
			im.extra = " /Decode [1 0 1 0 1 0 1 0]"
		}
	default:
		return nil, fmt.Errorf("不支持 %d 个通道的 JPEG", info.comps)
	}
	return im, nil
}

// flateImage 把解码后的图片压成 Flate（带 PNG 预测器），有透明部分时另外生成 SMask
func flateImage(m image.Image) *pdfImage {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	gray := isGrayImage(m)
	ch := 3
	if gray {
		ch = 1
	}
	pix := make([]byte, w*h*ch)
	alpha := make([]byte, w*h)
	opaque := true
	i, k := 0, 0
	put := func(r, g, bb, a byte) {
		if gray {
			pix[i] = r
			i++
		} else {
			pix[i], pix[i+1], pix[i+2] = r, g, bb
			i += 3
		}
		alpha[k] = a
		if a != 0xff {
			opaque = false
		}
		k++
	}
	switch t := m.(type) {
	case *image.NRGBA: // 常见的带透明 PNG，直接读像素
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := t.Pix[t.PixOffset(b.Min.X, y):]
			for x := 0; x < w; x++ {
				p := row[x*4 : x*4+4]
				put(p[0], p[1], p[2], p[3])
			}
		}
	case *image.RGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := t.Pix[t.PixOffset(b.Min.X, y):]
			for x := 0; x < w; x++ {
				p := row[x*4 : x*4+4]
				if p[3] == 0xff {
					put(p[0], p[1], p[2], 0xff)
				} else {
					c := color.NRGBAModel.Convert(color.RGBA{p[0], p[1], p[2], p[3]}).(color.NRGBA)
					put(c.R, c.G, c.B, c.A)
				}
			}
		}
	case *image.Gray:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := t.Pix[t.PixOffset(b.Min.X, y):]
			for x := 0; x < w; x++ {
				put(row[x], row[x], row[x], 0xff)
			}
		}
	case *image.Paletted:
		pal := make([]color.NRGBA, 256)
		for n, c := range t.Palette {
			pal[n] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := t.Pix[t.PixOffset(b.Min.X, y):]
			for x := 0; x < w; x++ {
				c := pal[row[x]]
				put(c.R, c.G, c.B, c.A)
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(m.At(x, y)).(color.NRGBA)
				put(c.R, c.G, c.B, c.A)
			}
		}
	}
	im := rawImage(pix, w, h, ch)
	if !opaque {
		im.smask = rawImage(alpha, w, h, 1)
	}
	return im
}

// isGrayImage 判断图片是不是灰度的（不必逐像素检查，看类型就够了）
func isGrayImage(m image.Image) bool {
	switch t := m.(type) {
	case *image.Gray, *image.Gray16:
		return true
	case *image.Paletted:
		for _, c := range t.Palette {
			r, g, b, _ := c.RGBA()
			if r != g || g != b {
				return false
			}
		}
		return true
	}
	return false
}

// rawImage 把 8 位像素数据（ch 个通道）压缩成 PDF 图片
func rawImage(pix []byte, w, h, ch int) *pdfImage {
	cs := "/DeviceRGB"
	if ch == 1 {
		cs = "/DeviceGray"
	}
	return &pdfImage{
		w: w, h: h, filter: "FlateDecode", cs: cs, bpc: 8,
		extra: fmt.Sprintf(" /DecodeParms << /Predictor 15 /Colors %d /BitsPerComponent 8 /Columns %d >>", ch, w),
		data:  pngPredict(pix, w, h, ch),
	}
}

// pngPredict 逐行挑一个 PNG 过滤器（让压缩率更高），然后用 zlib 压缩
func pngPredict(pix []byte, w, h, ch int) []byte {
	stride := w * ch
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
	prev := make([]byte, stride)
	cand := [5][]byte{}
	for f := range cand {
		cand[f] = make([]byte, stride+1)
		cand[f][0] = byte(f)
	}
	for y := 0; y < h; y++ {
		row := pix[y*stride : (y+1)*stride]
		best, bestSum := 0, -1
		for f := 0; f < 5; f++ {
			out := cand[f][1:]
			sum := 0
			for x := 0; x < stride; x++ {
				var a, b, c byte
				if x >= ch {
					a = row[x-ch]
					c = prev[x-ch]
				}
				b = prev[x]
				var v byte
				switch f {
				case 0:
					v = row[x]
				case 1:
					v = row[x] - a
				case 2:
					v = row[x] - b
				case 3:
					v = row[x] - byte((int(a)+int(b))/2)
				case 4:
					v = row[x] - paeth(a, b, c)
				}
				out[x] = v
				sum += int(int8(v)) * sign(int8(v))
			}
			if bestSum < 0 || sum < bestSum {
				best, bestSum = f, sum
			}
		}
		zw.Write(cand[best])
		prev = row
	}
	zw.Close()
	return buf.Bytes()
}

func sign(v int8) int {
	if v < 0 {
		return -1
	}
	return 1
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
