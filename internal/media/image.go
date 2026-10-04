package media

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// imgFmt 描述一种输出图片格式
type imgFmt struct {
	name  string // jpg png …
	ext   string // 输出扩展名
	coder string // magick 写文件时的格式前缀
	lossy bool   // 有损格式：按大小压缩时调质量
	alpha bool   // 能保存透明
	anim  bool   // 能保存动图
	defQ  int    // 默认质量
	minQ  int    // 按大小压缩时最低用到的质量
	downQ int    // 质量降到最低还太大、不得不缩小尺寸时用的质量
}

var imgFormats = map[string]imgFmt{
	"jpg":  {"jpg", ".jpg", "JPEG", true, false, false, 90, 10, 50},
	"png":  {"png", ".png", "PNG", false, true, false, 0, 0, 0},
	"webp": {"webp", ".webp", "WEBP", true, true, true, 85, 10, 50},
	"avif": {"avif", ".avif", "AVIF", true, true, false, 60, 5, 40},
	"jxl":  {"jxl", ".jxl", "JXL", true, true, false, 85, 10, 50},
	"bmp":  {"bmp", ".bmp", "BMP3", false, false, false, 0, 0, 0},
	"gif":  {"gif", ".gif", "GIF", false, true, true, 0, 0, 0},
	"tiff": {"tiff", ".tif", "TIFF", false, true, false, 0, 0, 0},
	"ico":  {"ico", ".ico", "ICO", false, true, false, 0, 0, 0},
	"tga":  {"tga", ".tga", "TGA", false, true, false, 0, 0, 0},
}

// sameImageExt：选「保持原格式」时，这些扩展名可以原样写回去。其他格式（HEIC、RAW、PSD、SVG……）改存成 JPG 或 PNG。
var sameImageExt = map[string]string{
	".jpg": "jpg", ".jpeg": "jpg", ".jpe": "jpg", ".jfif": "jpg",
	".png": "png", ".webp": "webp", ".avif": "avif", ".jxl": "jxl",
	".bmp": "bmp", ".dib": "bmp", ".gif": "gif", ".tif": "tiff", ".tiff": "tiff",
	".ico": "ico", ".tga": "tga",
}

// 矢量图：按需要的清晰度渲染
var vectorExt = map[string]bool{".svg": true, ".svgz": true, ".emf": true, ".wmf": true}

// 无损来源（%m 的值）：合成 PDF 时存成 PNG，免得文字、线条变糊
var losslessMagick = map[string]bool{
	"PNG": true, "PNG8": true, "PNG24": true, "PNG32": true, "PNG48": true, "PNG64": true, "APNG": true, "MNG": true,
	"GIF": true, "GIF87": true, "BMP": true, "BMP2": true, "BMP3": true, "TIFF": true, "TIFF64": true, "TGA": true,
	"ICO": true, "CUR": true, "PSD": true, "PSB": true, "SVG": true, "MSVG": true, "RSVG": true, "EMF": true, "WMF": true,
	"QOI": true, "PPM": true, "PGM": true, "PBM": true, "PNM": true, "PCX": true, "XCF": true,
}

// ---------------------------------------------------------------- 读图片信息

// imgInfo 是用 magick identify 读到的信息（选用的那一帧）
type imgInfo struct {
	format string // magick 的格式名：JPEG、PNG、HEIC……
	w, h   int    // 转正之后的尺寸
	frame  int    // 选用哪一帧
	frames int    // 一共几帧
	cmyk   bool
	alpha  bool
	icc    bool
	depth  int
	rotate bool // 照片里记着方向，需要转正
}

// animated 表示这是动图（多帧 GIF、WebP……），而不是多页 TIFF、多尺寸 ICO
func (i *imgInfo) animated() bool {
	if i.frames <= 1 {
		return false
	}
	switch i.format {
	case "GIF", "GIF87", "WEBP", "APNG", "MNG":
		return true
	}
	return false
}

func identifyImage(ctx context.Context, path string, pre []string) (*imgInfo, error) {
	args := []string{"identify"}
	for i := 0; i+1 < len(pre); i += 2 {
		if pre[i] == "-density" { // identify 不认 -background 之类的设置
			args = append(args, pre[i], pre[i+1])
		}
	}
	args = append(args, "-ping", "-format", "%m;%w;%h;%[orientation];%[colorspace];%[channels];%z;%[icc:description]\n", path)
	out, err := runMagick(ctx, args...)
	if err != nil {
		return nil, friendly(err, "读不出这张图片，它可能已经损坏，或者不是真正的图片文件")
	}
	var frames []imgInfo
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		p := strings.SplitN(line, ";", 8)
		if len(p) < 8 {
			continue
		}
		fi := imgInfo{format: strings.ToUpper(p[0]), cmyk: strings.EqualFold(p[4], "CMYK"), icc: strings.TrimSpace(p[7]) != ""}
		fi.w, _ = strconv.Atoi(p[1])
		fi.h, _ = strconv.Atoi(p[2])
		fi.depth, _ = strconv.Atoi(p[6])
		if ch := strings.Fields(p[5]); len(ch) > 0 {
			fi.alpha = strings.HasSuffix(strings.ToLower(ch[0]), "a")
		}
		switch p[3] {
		case "RightTop", "LeftBottom", "LeftTop", "RightBottom":
			fi.w, fi.h = fi.h, fi.w
			fi.rotate = true
		case "TopRight", "BottomRight", "BottomLeft":
			fi.rotate = true
		}
		fi.frame = len(frames)
		frames = append(frames, fi)
	}
	if len(frames) == 0 || frames[0].w <= 0 || frames[0].h <= 0 {
		return nil, conv.Fail("读不出这张图片，它可能已经损坏，或者不是真正的图片文件", strings.TrimSpace(string(out)))
	}
	best := frames[0]
	switch best.format {
	case "ICO", "CUR", "ICON":
		// 图标里有好几种尺寸，用最大的那个
		for _, f := range frames[1:] {
			if f.w*f.h > best.w*best.h {
				best = f
			}
		}
	}
	best.frames = len(frames)
	return &best, nil
}

// vectorPre 矢量图默认按 96 DPI 渲染，太小的话（比如 24×24 的图标）提高 DPI，让长边至少有 need 像素
func vectorPre(ctx context.Context, path string, need int) ([]string, error) {
	pre := []string{"-background", "none"}
	info, err := identifyImage(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	density := 96.0
	if l := max(info.w, info.h); l < need {
		density = math.Min(96*float64(need)/float64(l), 96*128)
	}
	return append([]string{"-density", strconv.Itoa(int(math.Ceil(density)))}, pre...), nil
}

// ---------------------------------------------------------------- 转换任务

// imgTask 是算好的一次图片转换：magick [pre] src [ops] [编码参数] 格式:输出
type imgTask struct {
	f      imgFmt
	src    string   // magick 输入（含帧选择）
	pre    []string // 读文件之前的设置
	ops    []string // 处理步骤：转正、颜色空间、缩放、去透明……
	w, h   int      // 处理之后的尺寸
	q      int      // 质量（有损格式）
	anim   bool     // 输出动图
	fixed  bool     // 尺寸被用户固定，压缩大小时不能改尺寸
	target int64    // 目标文件大小，0 = 不限
}

// Image 返回把图片转成 format 的转换器。format："same" 或 jpg png webp avif jxl bmp gif tiff ico tga。
func Image(format string) conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		return convertImage(ctx, j, strings.ToLower(format))
	}
}

func convertImage(ctx context.Context, j *conv.Job, format string) error {
	if err := tools.Need(tools.Magick()); err != nil {
		return err
	}
	j.Report(-1, "")
	tmp, err := j.TempDir()
	if err != nil {
		return err
	}
	in, err := safeInput(j.Input(), tmp)
	if err != nil {
		return conv.Fail("读不了这个文件", err.Error())
	}
	t, err := planImage(ctx, in, conv.Ext(j.Input()), format, j.Opt)
	if err != nil {
		return err
	}
	var res string
	if t.target > 0 {
		if res, err = t.fitSize(ctx, j, tmp); err != nil {
			return err
		}
	} else {
		res = filepath.Join(tmp, "out"+t.f.ext)
		if _, err := runMagick(ctx, t.args(res, nil)...); err != nil {
			return friendly(err, "图片转换失败")
		}
	}
	if fileSize(res) == 0 {
		return conv.Fail("图片转换失败，没有生成文件", "")
	}
	if err := moveFile(res, j.OutFile(t.f.ext)); err != nil {
		return friendly(err, "保存结果失败")
	}
	return nil
}

// planImage 读图片信息，算出转换步骤。ext 是原文件的扩展名（选「保持原格式」时用）。
func planImage(ctx context.Context, in, ext, format string, opt conv.Options) (*imgTask, error) {
	r := parseResize(opt)
	var pre []string
	if vectorExt[ext] {
		need := 1024
		if r.Mode == "box" {
			need = max(need, r.W, r.H)
		} else if r.Mode == "long" {
			need = max(need, r.Long)
		}
		var err error
		if pre, err = vectorPre(ctx, in, need); err != nil {
			return nil, err
		}
	}
	info, err := identifyImage(ctx, in, pre)
	if err != nil {
		return nil, err
	}

	// 输出格式
	var f imgFmt
	if format == "same" {
		if name, ok := sameImageExt[ext]; ok {
			f = imgFormats[name]
			f.ext = ext
		} else if info.alpha {
			f = imgFormats["png"]
		} else {
			f = imgFormats["jpg"]
		}
	} else {
		var ok bool
		if f, ok = imgFormats[format]; !ok {
			return nil, conv.Fail("不支持转成 "+format+" 格式", "")
		}
	}

	t := &imgTask{f: f, pre: pre, fixed: r.fixedBox(), target: max(opt.Int64(conv.OptTargetSize, 0), 0)}
	t.q = f.defQ
	if q := opt.Int(conv.OptQuality, 0); q >= 1 && q <= 100 && f.lossy {
		t.q = q
	}

	// 选哪些帧：动图转动图保留全部帧；多页 TIFF 转 TIFF 保留全部页；其余只取一帧
	t.anim = info.animated() && f.anim
	keepAll := t.anim || info.frames > 1 && f.name == "tiff" && strings.HasPrefix(info.format, "TIFF")
	t.src = in
	if !keepAll {
		t.src = fmt.Sprintf("%s[%d]", in, info.frame)
	}

	var ops []string
	if t.anim {
		ops = append(ops, "-coalesce") // 每一帧都变成完整画面，才能缩放、裁剪
	}
	ops = append(ops, "-auto-orient")
	srgb := filepath.Join(filepath.Dir(tools.Magick()), "sRGB.icc")
	_, iccErr := os.Stat(srgb)
	if info.cmyk {
		// 印刷用的 CMYK 图转成屏幕用的 sRGB；有色彩配置文件时按配置文件转，颜色更准
		if info.icc && iccErr == nil {
			ops = append(ops, "-profile", srgb)
		} else {
			ops = append(ops, "-colorspace", "sRGB")
		}
	}
	toSRGB := false
	switch f.name {
	case "png", "ico", "gif", "bmp", "tga":
		// 这些格式不支持（或者 libpng 不接受）iPhone 照片带的 Display P3 色彩配置文件，
		// 直接写会报「Incorrect data in iCCP」或者颜色发灰：先按配置文件把颜色换算成 sRGB
		toSRGB = info.icc && !info.cmyk && iccErr == nil
	}
	if opt.Bool(conv.OptStrip) {
		// 去掉拍摄信息。先把特殊色彩空间（比如苹果的 Display P3）转成 sRGB，颜色才不会变
		if info.icc && !info.cmyk && iccErr == nil {
			toSRGB = true
		}
	}
	if toSRGB {
		ops = append(ops, "-profile", srgb)
	}
	if opt.Bool(conv.OptStrip) {
		ops = append(ops, "-strip")
	}

	// 尺寸
	w, h := info.w, info.h
	if f.name == "ico" {
		// 图标：最大 256×256，补成正方形（透明边）
		g := r.plan(w, h, false)
		w, h = g.Out()
		ops = append(ops, resizeOps(g, info.w, info.h, "none")...)
		if l := max(w, h); l > 256 {
			ops = append(ops, "-resize", "256x256")
			w, h = max(1, int(math.Round(float64(w)*256/float64(l)))), max(1, int(math.Round(float64(h)*256/float64(l))))
		}
		if w != h {
			s := max(w, h)
			ops = append(ops, "-background", "none", "-gravity", "center", "-extent", fmt.Sprintf("%dx%d", s, s))
			w, h = s, s
		}
	} else {
		// 补边颜色：用户指定的优先；否则原图透明且新格式支持透明时补透明边，其余补白边
		bg := r.BG
		if bg == "" {
			bg = "#ffffff"
			if f.alpha && info.alpha {
				bg = "none"
			}
		}
		if bg == "none" && !f.alpha {
			bg = "#ffffff"
		}
		g := r.plan(w, h, false)
		ops = append(ops, resizeOps(g, w, h, bg)...)
		w, h = g.Out()
	}
	t.w, t.h = w, h

	if !f.alpha {
		// JPG、BMP 不支持透明：铺到白底（或者用户选的颜色）上
		flat := "#ffffff"
		if r.BG != "" && r.BG != "none" {
			flat = r.BG
		}
		ops = append(ops, "-background", flat, "-alpha", "remove", "-alpha", "off")
	}
	keep16 := (f.name == "png" || f.name == "tiff") && info.depth > 8 && losslessMagick[info.format]
	if !keep16 {
		ops = append(ops, "-depth", "8")
	}
	t.ops = ops
	return t, nil
}

// resizeOps 把缩放步骤写成 magick 参数。w×h 是缩放前的尺寸，bg 是补边颜色。
func resizeOps(g geometry, w, h int, bg string) []string {
	var a []string
	if g.SW != w || g.SH != h {
		a = append(a, "-resize", fmt.Sprintf("%dx%d!", g.SW, g.SH))
	}
	if g.CW > 0 {
		a = append(a, "-gravity", "center", "-crop", fmt.Sprintf("%dx%d+0+0", g.CW, g.CH), "+repage")
	}
	if g.PW > 0 {
		a = append(a, "-background", bg, "-gravity", "center", "-extent", fmt.Sprintf("%dx%d", g.PW, g.PH))
	}
	return a
}

// encodeArgs 是写文件时的格式参数。w×h 是最终尺寸，q 是质量，colors > 0 表示减少到这么多种颜色。
func (t *imgTask) encodeArgs(w, h, q, colors int) []string {
	var a []string
	switch t.f.name {
	case "jpg", "webp", "avif", "jxl":
		a = append(a, "-quality", strconv.Itoa(q))
	case "png":
		if colors > 0 {
			a = append(a, "-dither", "FloydSteinberg", "-colors", strconv.Itoa(colors))
		}
		// 压缩级别 5：照片比默认快两三倍，截图类只大 1% 左右
		a = append(a, "-define", "png:exclude-chunks=date,time", "-define", "png:compression-level=5")
	case "gif":
		if colors > 0 {
			a = append(a, "-colors", strconv.Itoa(colors))
		}
		if t.anim {
			a = append(a, "-layers", "optimize")
		}
	case "tiff":
		a = append(a, "-compress", "LZW")
	case "ico":
		a = append(a, "-define", "icon:auto-resize="+icoSizes(max(w, h)), "-define", "icon:png-compression-size=256")
	}
	return a
}

// icoSizes 是图标里要放的尺寸：不超过原图的常用尺寸，原图不是常用尺寸时也放一份原尺寸
func icoSizes(s int) string {
	var out []string
	std := false
	for _, v := range []int{256, 128, 64, 48, 32, 16} {
		if v == s {
			std = true
		}
		if v <= s {
			out = append(out, strconv.Itoa(v))
		}
	}
	if !std {
		out = append([]string{strconv.Itoa(min(s, 256))}, out...)
	}
	return strings.Join(out, ",")
}

// args 是完整的 magick 参数；extra 插在处理步骤和编码参数之间
func (t *imgTask) args(out string, extra []string) []string {
	a := append([]string{}, t.pre...)
	a = append(a, t.src)
	a = append(a, t.ops...)
	a = append(a, extra...)
	a = append(a, t.encodeArgs(t.w, t.h, t.q, 0)...)
	return append(a, t.f.coder+":"+out)
}

// ---------------------------------------------------------------- 压缩到指定大小

// fitSize 把图片压到 t.target 字节以内，返回临时文件夹里的结果。
// 有损格式先降质量（二分），质量降到底还太大再缩小尺寸；无损格式先减少颜色（PNG、GIF），再缩小尺寸。
// 用户固定了输出尺寸（裁剪、拉伸、补边）时不改尺寸，压不到就报错。
func (t *imgTask) fitSize(ctx context.Context, j *conv.Job, tmp string) (string, error) {
	target := t.target
	note := "正在压缩到 " + humanSize(target) + "…"
	const steps = 10.0
	step := 0
	j.Report(0, note)

	// 先做出一个处理好的中间文件（转正、缩放、去透明都做完），之后每次只是换参数重新编码，快很多
	prep := filepath.Join(tmp, "prep.miff")
	a := append([]string{}, t.pre...)
	a = append(a, t.src)
	a = append(a, t.ops...)
	if _, err := runMagick(ctx, append(a, "MIFF:"+prep)...); err != nil {
		return "", friendly(err, "图片转换失败")
	}
	step++
	j.Report(float64(step)/steps, note)

	smallest := int64(math.MaxInt64)
	n := 0
	// try 用质量 q、缩放比例 scale、颜色数 colors 编码一次，返回文件和大小
	try := func(scale float64, q, colors int) (string, int64, error) {
		w, h := t.w, t.h
		var extra []string
		if scale < 1 {
			w, h = max(1, int(math.Round(float64(t.w)*scale))), max(1, int(math.Round(float64(t.h)*scale)))
			extra = []string{"-resize", fmt.Sprintf("%dx%d!", w, h)}
		}
		n++
		out := filepath.Join(tmp, fmt.Sprintf("try%d%s", n, t.f.ext))
		a := append([]string{prep}, extra...)
		a = append(a, t.encodeArgs(w, h, q, colors)...)
		if _, err := runMagick(ctx, append(a, t.f.coder+":"+out)...); err != nil {
			return "", 0, friendly(err, "图片压缩失败")
		}
		sz := fileSize(out)
		if sz <= 0 {
			return "", 0, conv.Fail("图片压缩失败，没有生成文件", "")
		}
		smallest = min(smallest, sz)
		step++
		j.Report(math.Min(float64(step)/steps, 0.97), note)
		return out, sz, nil
	}
	tooBig := func() error {
		if t.fixed {
			return conv.Fail(fmt.Sprintf("压不到 %s：尺寸固定为 %d×%d 时最小只能压到 %s", humanSize(target), t.w, t.h, humanSize(smallest)),
				"可以把尺寸改小一些，或者把文件大小放宽一些")
		}
		return conv.Fail(fmt.Sprintf("压不到 %s，最小只能压到 %s", humanSize(target), humanSize(smallest)), "")
	}

	if t.f.lossy {
		qmax := t.q
		p, s, err := try(1, qmax, 0)
		if err != nil || s <= target {
			return p, err
		}
		qmin := min(t.f.minQ, qmax)
		if qmin < qmax {
			pl, sl, err := try(1, qmin, 0)
			if err != nil {
				return "", err
			}
			if sl <= target {
				// 在 qmin（够小）和 qmax（太大）之间找最高的合格质量：先按大小的对数插值猜，后面改二分
				lo, hi, sLo, sHi, best := qmin, qmax, sl, s, pl
				for i := 0; i < 6 && hi-lo > 1 && float64(sLo) < 0.95*float64(target); i++ {
					q := (lo + hi) / 2
					if i < 2 && sHi > sLo {
						fr := (math.Log(float64(target)) - math.Log(float64(sLo))) / (math.Log(float64(sHi)) - math.Log(float64(sLo)))
						q = lo + int(math.Round(fr*float64(hi-lo)))
					}
					q = min(max(q, lo+1), hi-1)
					p, s, err := try(1, q, 0)
					if err != nil {
						return "", err
					}
					if s <= target {
						lo, sLo, best = q, s, p
					} else {
						hi, sHi = q, s
					}
				}
				return best, nil
			}
			s = sl
		}
		if t.fixed {
			return "", tooBig()
		}
		// 质量降到最低还太大，只能缩小尺寸。这时用一个中等质量：小而清楚比大而满是马赛克好看
		q := min(t.f.downQ, qmax)
		return t.shrink(ctx, try, q, 0, s, tooBig)
	}

	// 无损格式
	p, s, err := try(1, 0, 0)
	if err != nil || s <= target {
		return p, err
	}
	colors := 0
	switch t.f.name {
	case "png":
		colors = 256
	case "gif":
		colors = 128
	}
	if colors > 0 {
		p, s2, err := try(1, 0, colors)
		if err != nil || s2 <= target {
			return p, err
		}
		if s2 < s {
			s = s2
		} else {
			colors = 0 // 颜色很杂的图减少颜色反而更大（抖动噪点），缩小尺寸时就不减了
		}
	}
	if t.fixed {
		return "", tooBig()
	}
	return t.shrink(ctx, try, 0, colors, s, tooBig)
}

// shrink 缩小尺寸直到不超过目标大小。size1 是不缩小时（或者质量更低时）的大小，用来猜第一次的比例。
func (t *imgTask) shrink(ctx context.Context, try func(float64, int, int) (string, int64, error), q, colors int, size1 int64, tooBig func() error) (string, error) {
	target := t.target
	long := float64(max(t.w, t.h))
	minScale := math.Min(1, 16/long) // 长边最小 16 像素
	lo, hi := 0.0, 1.0               // lo：已知合格的最大比例；hi：已知太大的最小比例
	best := ""
	s, sz := 1.0, size1
	for i := 0; i < 7; i++ {
		if ctx.Err() != nil {
			return "", conv.ErrCancelled
		}
		// 文件大小大致和像素数成正比
		next := s * math.Sqrt(float64(target)/float64(sz)) * 0.97
		if next >= hi || next <= lo {
			next = (lo + hi) / 2
			if lo == 0 {
				next = hi * 0.6
			}
		}
		next = math.Max(next, minScale)
		if next >= hi { // 已经到最小尺寸了还是太大
			break
		}
		p, size, err := try(next, q, colors)
		if err != nil {
			return "", err
		}
		if size <= target {
			lo, best = next, p
			// 已经很接近目标或者差距不到 4%，够好了
			if float64(size) > 0.9*float64(target) || (hi-lo)/hi < 0.04 {
				break
			}
		} else {
			hi = next
		}
		s, sz = next, size
		if best != "" && i >= 4 {
			break
		}
	}
	if best == "" {
		return "", tooBig()
	}
	return best, nil
}

// ---------------------------------------------------------------- 合成 PDF 用

// PrepareForPDF 把一张图片整理成能直接放进 PDF 的 JPEG 或 PNG，写在 tmpDir 里，返回文件路径。
// 会转正、按选项缩放、把 CMYK 转成 sRGB；透明图和无损来源（PNG、截图……）存成 PNG，照片存成 JPEG。
// 不需要任何改动的 JPEG、PNG 直接返回原路径（不会改动原文件）。
func PrepareForPDF(ctx context.Context, in string, opt conv.Options, tmpDir string) (string, error) {
	if err := tools.Need(tools.Magick()); err != nil {
		return "", err
	}
	src, err := safeInput(in, tmpDir)
	if err != nil {
		return "", conv.Fail("读不了这个文件", err.Error())
	}
	r := parseResize(opt)
	var pre []string
	if vectorExt[conv.Ext(in)] {
		if pre, err = vectorPre(ctx, src, 2048); err != nil {
			return "", err
		}
	}
	info, err := identifyImage(ctx, src, pre)
	if err != nil {
		return "", err
	}
	g := r.plan(info.w, info.h, false)
	resizing := g != geometry{SW: info.w, SH: info.h}
	if !resizing && !info.rotate && !info.cmyk && (info.format == "JPEG" || info.format == "PNG" && info.frames == 1) {
		return in, nil
	}

	asPNG := info.alpha || losslessMagick[info.format]
	ops := append([]string{}, pre...)
	ops = append(ops, fmt.Sprintf("%s[%d]", src, info.frame), "-auto-orient")
	if info.cmyk {
		srgb := filepath.Join(filepath.Dir(tools.Magick()), "sRGB.icc")
		if _, err := os.Stat(srgb); err == nil && info.icc {
			ops = append(ops, "-profile", srgb)
		} else {
			ops = append(ops, "-colorspace", "sRGB")
		}
	}
	bg := r.BG
	if bg == "" || bg == "none" && !asPNG {
		bg = "#ffffff"
	}
	ops = append(ops, resizeOps(g, info.w, info.h, bg)...)
	ext, coder := ".jpg", "JPEG"
	if asPNG {
		ext, coder = ".png", "PNG"
		ops = append(ops, "-define", "png:exclude-chunks=date,time", "-define", "png:compression-level=5")
	} else {
		q := opt.Int(conv.OptQuality, 0)
		if q < 1 || q > 100 {
			q = 92
		}
		ops = append(ops, "-background", "#ffffff", "-alpha", "remove", "-alpha", "off", "-quality", strconv.Itoa(q))
	}
	ops = append(ops, "-depth", "8")

	f, err := os.CreateTemp(tmpDir, "pdfimg-*"+ext)
	if err != nil {
		return "", err
	}
	out := f.Name()
	f.Close()
	if _, err := runMagick(ctx, append(ops, coder+":"+out)...); err != nil {
		os.Remove(out)
		return "", friendly(err, "图片处理失败："+filepath.Base(in))
	}
	return out, nil
}
