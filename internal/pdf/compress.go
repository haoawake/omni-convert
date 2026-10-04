package pdf

// PDF 压缩。
//   light  无损：pdfcpu 优化（去掉重复的字体、图片和没用的对象，对象流压缩），再把没压缩的数据流压一下
//   medium 推荐：在 light 的基础上，把大图缩到长边不超过 2000 像素、重新存成 JPEG（质量 70），文字和矢量图不动
//   strong 强力：每页渲染成 110 dpi 的 JPEG（质量 60）重新拼成 PDF，文字不能再选中

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 各档压缩的参数
const (
	mediumMaxSide = 2000
	mediumQuality = 70
	strongDPI     = 110
	strongQuality = 60
)

// Compress 压缩 PDF。level 选项：light | medium（默认）| strong。
// 结束时用 j.Report 汇报压缩前后的大小；压不小的话原样输出一份，并说明「已经很小了」。
func Compress() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		in := j.Input()
		pw := j.Opt.Str(conv.OptPassword, "")
		before := fileSize(in)
		tmp, err := j.TempDir()
		if err != nil {
			return conv.Fail("没法创建临时文件夹", err.Error())
		}
		cand := filepath.Join(tmp, "compressed.pdf")

		switch strings.ToLower(j.Opt.Str(conv.OptLevel, "medium")) {
		case "light", "lossless", "low":
			j.Report(-1, "正在无损压缩")
			err = withRepair(ctx, j, in, pw, func(src, pw string) error {
				return optimizePDF(ctx, j, src, pw, cand, false)
			})
		case "strong", "high", "max":
			err = rasterizePDF(ctx, j, in, pw, cand)
		default:
			j.Report(-1, "正在分析")
			err = withRepair(ctx, j, in, pw, func(src, pw string) error {
				return optimizePDF(ctx, j, src, pw, cand, true)
			})
		}
		if err != nil {
			return err
		}
		if err := cancelled(ctx); err != nil {
			return err
		}

		out := j.OutFileNamed(j.Base()+"_压缩", ".pdf")
		after := fileSize(cand)
		if after <= 0 || after >= before {
			if err := copyFile(in, out); err != nil {
				return err
			}
			j.Report(1, fmt.Sprintf("已经很小了，没法再压（%s）", conv.HumanSize(before)))
			return nil
		}
		if err := moveFile(cand, out); err != nil {
			return err
		}
		pct := min(99, 100-after*100/max(before, 1))
		if pct < 3 {
			j.Report(1, fmt.Sprintf("已经很小了，没法再压多少（%s → %s）", conv.HumanSize(before), conv.HumanSize(after)))
			return nil
		}
		j.Report(1, fmt.Sprintf("从 %s 压缩到 %s（小了 %d%%）", conv.HumanSize(before), conv.HumanSize(after), pct))
		return nil
	})
}

// optimizePDF 用 pdfcpu 读入、优化，images 为 true 时重新压缩图片，然后写到 out
func optimizePDF(c context.Context, j *conv.Job, in, pw, out string, images bool) error {
	ctx, err := readCtx(c, in, newConf(pw, model.OPTIMIZE))
	if err != nil {
		return err
	}
	if images {
		if err := recompressImages(c, j, ctx); err != nil {
			return err
		}
	}
	compressPlainStreams(ctx)
	if err := cancelled(c); err != nil {
		return err
	}
	j.Report(-1, "正在保存")
	return writeFile(out, func(w io.Writer) error { return api.WriteContext(c, ctx, w) })
}

// compressPlainStreams 把没有压缩过的数据流（常见于老软件生成的页面内容）用 Flate 压缩
func compressPlainStreams(ctx *model.Context) {
	for _, e := range ctx.XRefTable.Table {
		if e == nil || e.Free {
			continue
		}
		sd, ok := e.Object.(types.StreamDict)
		if !ok || len(sd.FilterPipeline) > 0 || sd.Dict["Filter"] != nil || len(sd.Raw) < 256 {
			continue
		}
		if t := sd.Type(); t != nil && (*t == "Metadata" || *t == "XRef" || *t == "ObjStm") {
			continue
		}
		var b bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&b, zlib.BestCompression)
		zw.Write(sd.Raw)
		zw.Close()
		if b.Len() >= len(sd.Raw)*9/10 {
			continue
		}
		setStream(&sd, b.Bytes(), filter.Flate)
		e.Object = sd
	}
}

// setStream 换掉数据流的内容和过滤器
func setStream(sd *types.StreamDict, data []byte, filterName string) {
	sd.Raw = data
	sd.Content = nil
	l := int64(len(data))
	sd.StreamLength = &l
	sd.StreamLengthObjNr = nil
	sd.FilterPipeline = []types.PDFFilter{{Name: filterName}}
	sd.Dict["Filter"] = types.Name(filterName)
	sd.Dict["Length"] = types.Integer(l)
	delete(sd.Dict, "DecodeParms")
}

// ---------------------------------------------------------------- 图片重新压缩

func isImage(d types.Dict) bool {
	st := d.Subtype()
	return st != nil && *st == "Image"
}

// recompressImages 把文档里的大图缩小、重新存成 JPEG（只有变小 10% 以上才替换）
func recompressImages(c context.Context, j *conv.Job, ctx *model.Context) error {
	xt := ctx.XRefTable
	masks := map[int]bool{} // 被当作蒙版用的图片不动
	var cands []int
	for nr, e := range xt.Table {
		if e == nil || e.Free {
			continue
		}
		sd, ok := e.Object.(types.StreamDict)
		if !ok || !isImage(sd.Dict) {
			continue
		}
		for _, k := range []string{"SMask", "Mask"} {
			if ir := sd.IndirectRefEntry(k); ir != nil {
				masks[ir.ObjectNumber.Value()] = true
			}
		}
		cands = append(cands, nr)
	}
	sort.Ints(cands)
	for i, nr := range cands {
		if err := cancelled(c); err != nil {
			return err
		}
		if masks[nr] {
			continue
		}
		j.Report(float64(i)/float64(len(cands)), fmt.Sprintf("正在压缩图片 %d/%d", i+1, len(cands)))
		e := xt.Table[nr]
		sd := e.Object.(types.StreamDict)
		if data, w, h, ok := recompressImage(xt, &sd); ok {
			setStream(&sd, data, filter.DCT)
			sd.Dict["Width"] = types.Integer(w)
			sd.Dict["Height"] = types.Integer(h)
			sd.Dict["BitsPerComponent"] = types.Integer(8)
			e.Object = sd
		}
	}
	return nil
}

// intEntry 读一个整数项（可能是间接引用）
func intEntry(xt *model.XRefTable, d types.Dict, key string) int {
	o, ok := d[key]
	if !ok {
		return 0
	}
	i, err := xt.DereferenceInteger(o)
	if err != nil || i == nil {
		return 0
	}
	return i.Value()
}

// imageComps 判断颜色空间：返回 1（灰度）、3（RGB）或 0（不处理的颜色空间）
func imageComps(xt *model.XRefTable, o types.Object) int {
	o, err := xt.Dereference(o)
	if err != nil || o == nil {
		return 0
	}
	switch v := o.(type) {
	case types.Name:
		switch v {
		case "DeviceRGB", "RGB", "CalRGB":
			return 3
		case "DeviceGray", "G", "CalGray":
			return 1
		}
	case types.Array:
		if len(v) < 2 {
			return 0
		}
		n, ok := v[0].(types.Name)
		if !ok {
			return 0
		}
		switch n {
		case "CalRGB":
			return 3
		case "CalGray":
			return 1
		case "ICCBased":
			sd, _, err := xt.DereferenceStreamDict(v[1])
			if err != nil || sd == nil {
				return 0
			}
			switch intEntry(xt, sd.Dict, "N") {
			case 1:
				return 1
			case 3:
				return 3
			}
		}
	}
	return 0
}

// recompressImage 试着重新压缩一张图片，返回新的 JPEG 数据和尺寸；不值得或不能处理时 ok 为 false
func recompressImage(xt *model.XRefTable, sd *types.StreamDict) (data []byte, nw, nh int, ok bool) {
	defer func() {
		if recover() != nil { // 遇到奇怪的数据宁可不压，也不能让整个任务崩掉
			ok = false
		}
	}()
	d := sd.Dict
	if b := d.BooleanEntry("ImageMask"); b != nil && *b {
		return nil, 0, 0, false
	}
	if _, has := d["Decode"]; has {
		return nil, 0, 0, false
	}
	if m, has := d["Mask"]; has {
		if _, isRef := m.(types.IndirectRef); !isRef { // 颜色键蒙版要求颜色精确，不能有损压缩
			return nil, 0, 0, false
		}
	}
	if o, has := d["SMask"]; has {
		if sm, _, err := xt.DereferenceStreamDict(o); err == nil && sm != nil {
			if _, matte := sm.Dict["Matte"]; matte {
				return nil, 0, 0, false
			}
		}
	}
	w, h := intEntry(xt, d, "Width"), intEntry(xt, d, "Height")
	if w <= 0 || h <= 0 || intEntry(xt, d, "BitsPerComponent") != 8 {
		return nil, 0, 0, false
	}
	if w*h < 128*128 || len(sd.Raw) < 16*1024 || w*h > 200_000_000 {
		return nil, 0, 0, false
	}
	ch := imageComps(xt, d["ColorSpace"])
	if ch == 0 {
		return nil, 0, 0, false
	}

	var img image.Image
	var pix []byte
	isDCT := false
	switch {
	case len(sd.FilterPipeline) == 1 && sd.FilterPipeline[0].Name == filter.DCT:
		m, err := jpeg.Decode(bytes.NewReader(sd.Raw))
		if err != nil || m.Bounds().Dx() != w || m.Bounds().Dy() != h {
			return nil, 0, 0, false
		}
		if _, cmyk := m.(*image.CMYK); cmyk {
			return nil, 0, 0, false
		}
		img, isDCT = m, true
	default:
		for _, f := range sd.FilterPipeline {
			switch f.Name {
			case filter.Flate, filter.LZW, filter.ASCII85, filter.ASCIIHex, filter.RunLength:
			default:
				return nil, 0, 0, false
			}
		}
		cp := *sd
		cp.Content = nil
		if err := cp.Decode(); err != nil || len(cp.Content) < w*h*ch {
			return nil, 0, 0, false
		}
		pix = cp.Content[:w*h*ch]
	}

	nw, nh = w, h
	if l := max(w, h); l > mediumMaxSide {
		s := float64(mediumMaxSide) / float64(l)
		nw, nh = max(1, int(float64(w)*s+0.5)), max(1, int(float64(h)*s+0.5))
	}
	var out image.Image
	switch {
	case isDCT && nw == w && nh == h:
		out = img // 尺寸不变，直接用解码出来的 YCbCr 重新编码
	default:
		if pix == nil {
			pix = packPixels(img, ch)
		}
		out = unpack(downscale(pix, w, h, ch, nw, nh), nw, nh, ch)
	}
	if ch == 1 {
		if _, gray := out.(*image.Gray); !gray {
			out = unpack(packPixels(out, 1), nw, nh, 1)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, out, &jpeg.Options{Quality: mediumQuality}); err != nil {
		return nil, 0, 0, false
	}
	if b.Len() >= len(sd.Raw)*9/10 {
		return nil, 0, 0, false
	}
	return b.Bytes(), nw, nh, true
}

// ---------------------------------------------------------------- 强力压缩

// jpegPage 是渲染好并压成 JPEG 的一页
type jpegPage struct {
	data []byte
	w, h int
	gray bool
}

// rasterizePDF 把每页渲染成图片重新拼成 PDF（页面大小不变）
func rasterizePDF(c context.Context, j *conv.Job, in, pw, out string) error {
	d, err := Open(in, pw)
	if err != nil {
		return err
	}
	defer d.Close()
	n := d.PageCount()
	pages := make([]int, n)
	for i := range pages {
		pages[i] = i
	}
	sizes := make([][2]float64, n)
	for i := range sizes {
		w, h := d.PageSize(i)
		sizes[i] = [2]float64{w, h}
	}
	return writeFile(out, func(w io.Writer) error {
		p := newImgPDF(w)
		render := func(_, page int) (*image.RGBA, error) { return d.Render(page, strongDPI) }
		encode := func(k int, img *image.RGBA) (any, error) {
			m := grayIfNeutral(img)
			var b bytes.Buffer
			if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: strongQuality}); err != nil {
				return nil, err
			}
			_, gray := m.(*image.Gray)
			return jpegPage{b.Bytes(), img.Rect.Dx(), img.Rect.Dy(), gray}, nil
		}
		emit := func(k int, v any) error {
			jp := v.(jpegPage)
			cs := "/DeviceRGB"
			if jp.gray {
				cs = "/DeviceGray"
			}
			im := &pdfImage{w: jp.w, h: jp.h, filter: "DCTDecode", cs: cs, bpc: 8, data: jp.data}
			pw, ph := sizes[k][0], sizes[k][1]
			return p.addPage(im, pw, ph, 0, 0, pw, ph)
		}
		maxPx := maxPagePixels(d, pages, strongDPI/72.0)
		if err := pipeline(c, j, pages, render, encode, emit, workersFor(maxPx)); err != nil {
			return err
		}
		return p.close()
	})
}
