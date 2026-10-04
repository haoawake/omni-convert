// Package media 负责图片、视频、音频的转换：图片用 ImageMagick，视频和音频用 ffmpeg。
//
// 对外的入口：
//
//	Image(format)   图片转成 jpg / png / webp / avif / jxl / bmp / gif / tiff / ico / tga，或者保持原格式
//	Video(format)   视频转成 mp4 / mkv / mov / avi / webm / flv / wmv / m4v / ts / mpg / 3gp / gif / webp
//	Audio(format)   音频（也可以是视频里的声音）转成 mp3 / m4a / aac / wav / flac / ogg / opus / wma / aiff / ac3 / amr / m4r
//	PrepareForPDF   把一张图片整理成能直接放进 PDF 的 JPEG 或 PNG（图片合成 PDF 用）
//	Probe           读视频、音频的时长、尺寸、编码等信息
//	GPUEncoders     看看这台电脑的显卡能用来编码哪些格式
//
// 几个约定：
//   - 结果都通过 j.OutFile 写出（先写到 j.TempDir 里，成功后再移过去），绝不改动原文件。
//   - 图片补边默认白色；原图透明、新格式也支持透明（PNG、WebP……）时补透明边；图标（ICO）总是透明边。视频补边默认黑色。
//   - 视频编码对得上、又没改尺寸/帧率/截取时直接复制画面（MKV 转 MP4 几秒钟完成），选「低画质」时才强制重新编码。
//   - 选了显卡编码但显卡编不了时自动改用 CPU；HDR（PQ、HLG）视频转成普通画面时自动做色调映射。
//   - HEIC 只能读不能写（ImageMagick 没有 HEIC 编码器）。
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// ---------------------------------------------------------------- 尺寸

// resizeSpec 是界面上的「尺寸」设置
type resizeSpec struct {
	Mode    string // none | box | percent | long
	W, H    int    // box：目标宽高，其中一个可以是 0（按比例）
	Fit     string // contain | cover | stretch | pad
	Percent float64
	Long    int
	BG      string // 补边颜色（#rrggbb、#rrggbbaa 或 none）；空 = 用各自的默认值
}

func parseResize(opt conv.Options) resizeSpec {
	r := resizeSpec{
		Mode:    strings.ToLower(opt.Str(conv.OptResize, "")),
		W:       max(opt.Int(conv.OptWidth, 0), 0),
		H:       max(opt.Int(conv.OptHeight, 0), 0),
		Fit:     strings.ToLower(opt.Str(conv.OptFit, "contain")),
		Percent: opt.Float(conv.OptPercent, 0),
		Long:    opt.Int(conv.OptLong, 0),
		BG:      parseColor(opt.Str(conv.OptPadColor, "")),
	}
	if r.Mode == "" && (r.W > 0 || r.H > 0) {
		r.Mode = "box" // 只填了宽高也当成指定宽高
	}
	switch r.Fit {
	case "contain", "cover", "stretch", "pad":
	default:
		r.Fit = "contain"
	}
	switch r.Mode {
	case "box":
		if r.W <= 0 && r.H <= 0 {
			r.Mode = "none"
		}
	case "percent":
		r.Percent = min(r.Percent, 1000)
		if r.Percent <= 0 || r.Percent == 100 {
			r.Mode = "none"
		}
	case "long":
		if r.Long <= 0 {
			r.Mode = "none"
		}
	default:
		r.Mode = "none"
	}
	return r
}

// active 表示要改尺寸
func (r resizeSpec) active() bool { return r.Mode != "none" }

// fixedBox 表示用户要求输出正好 W×H（裁剪、拉伸、补边）。这时为了压缩大小也不能改尺寸。
func (r resizeSpec) fixedBox() bool {
	return r.Mode == "box" && r.W > 0 && r.H > 0 && r.Fit != "contain"
}

// geometry 是算好的缩放步骤：先缩放到 SW×SH，再从中间裁出 CW×CH，或者补边到 PW×PH
type geometry struct {
	SW, SH int
	CW, CH int // 0 = 不裁
	PW, PH int // 0 = 不补边
}

// Out 是最终输出的尺寸
func (g geometry) Out() (int, int) {
	switch {
	case g.CW > 0:
		return g.CW, g.CH
	case g.PW > 0:
		return g.PW, g.PH
	}
	return g.SW, g.SH
}

// plan 根据原尺寸 w×h 算出缩放步骤。even 为 true 时所有尺寸都取偶数（yuv420p 视频编码的要求）。
func (r resizeSpec) plan(w, h int, even bool) geometry {
	w, h = max(w, 1), max(h, 1)
	round := func(v float64) int {
		if even {
			return max(2, int(math.Round(v/2))*2)
		}
		return max(1, int(math.Round(v)))
	}
	ceil := func(v float64) int {
		v -= 1e-9 // 避免 400.0000001 这种被向上取成 401
		if even {
			return max(2, int(math.Ceil(v/2))*2)
		}
		return max(1, int(math.Ceil(v)))
	}
	fw, fh := float64(w), float64(h)
	g := geometry{SW: w, SH: h}
	scale := func(s float64) { g.SW, g.SH = round(fw*s), round(fh*s) }
	switch r.Mode {
	case "percent":
		scale(r.Percent / 100)
	case "long":
		if l := max(w, h); l > r.Long {
			scale(float64(r.Long) / float64(l))
		}
	case "box":
		bw, bh := float64(r.W), float64(r.H)
		switch {
		case r.H <= 0:
			scale(bw / fw)
		case r.W <= 0:
			scale(bh / fh)
		case r.Fit == "stretch":
			g.SW, g.SH = round(bw), round(bh)
		case r.Fit == "cover":
			cw, ch := round(bw), round(bh)
			s := math.Max(bw/fw, bh/fh)
			g.SW, g.SH = max(ceil(fw*s), cw), max(ceil(fh*s), ch)
			if g.SW != cw || g.SH != ch {
				g.CW, g.CH = cw, ch
			}
		default: // contain、pad
			s := math.Min(bw/fw, bh/fh)
			g.SW, g.SH = min(round(fw*s), round(bw)), min(round(fh*s), round(bh))
			if r.Fit == "pad" {
				if pw, ph := round(bw), round(bh); pw != g.SW || ph != g.SH {
					g.PW, g.PH = pw, ph
				}
			}
		}
	}
	return g
}

var (
	reHex6 = regexp.MustCompile(`^#?([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	reHex3 = regexp.MustCompile(`^#?[0-9a-fA-F]{3}$`)
)

// parseColor 把界面上填的颜色整理成 #rrggbb（或 #rrggbbaa、none），认不出来返回空字符串
func parseColor(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return ""
	case "none", "transparent", "透明":
		return "none"
	case "white", "白", "白色":
		return "#ffffff"
	case "black", "黑", "黑色":
		return "#000000"
	}
	if reHex6.MatchString(s) {
		return "#" + strings.TrimPrefix(s, "#")
	}
	if reHex3.MatchString(s) {
		s = strings.TrimPrefix(s, "#")
		return "#" + string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	return ""
}

// ---------------------------------------------------------------- 外部程序

// runMagick 运行 magick，不显示警告。第一个参数可以是 identify 这样的子命令。
func runMagick(ctx context.Context, args ...string) ([]byte, error) {
	var a []string
	if len(args) > 0 && args[0] == "identify" {
		a = append([]string{"identify", "-quiet"}, args[1:]...)
	} else {
		a = append([]string{"-quiet"}, args...)
	}
	return tools.Run(ctx, tools.Magick(), a, nil)
}

// runFFmpeg 运行 ffmpeg 并解析进度。dur 是输出应有的时长（秒，用来算进度，0 = 不知道），
// onFrac 收到 0~1 的进度。dir 是工作目录（两遍编码的日志文件放在这里），可以为空。
func runFFmpeg(ctx context.Context, args []string, dir string, dur float64, onFrac func(float64)) error {
	base := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-progress", "pipe:1", "-nostats"}
	ro := &tools.RunOptions{Dir: dir}
	if onFrac != nil {
		ro.OnStdout = func(line string) {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || dur <= 0 {
				return
			}
			switch k {
			case "out_time_us", "out_time_ms": // 两个其实都是微秒
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					onFrac(min(float64(n)/1e6/dur, 0.999))
				}
			}
		}
	}
	_, err := tools.Run(ctx, tools.FFmpeg(), append(base, args...), ro)
	return err
}

// ffPath 给 ffmpeg 的文件路径加上 file: 前缀，免得文件名被当成别的协议
func ffPath(p string) string { return "file:" + p }

// toolDetail 取出外部程序报错的最后一行，当作「详细信息」
func toolDetail(err error) string {
	if te, ok := tools.AsToolError(err); ok {
		return te.LastLine()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// friendly 把外部程序的错误变成给用户看的错误。取消和已经是 UserError 的错误原样返回。
func friendly(err error, msg string) error {
	if err == nil {
		return nil
	}
	if isCancel(err) {
		return conv.ErrCancelled
	}
	var ue *conv.UserError
	if errors.As(err, &ue) {
		return err
	}
	detail := toolDetail(err)
	low := strings.ToLower(detail)
	switch {
	case strings.Contains(low, "no space left") || strings.Contains(low, "not enough space") || strings.Contains(low, "disk full"):
		return conv.Fail("磁盘空间不够了，请清理一下输出文件夹所在的磁盘", detail)
	case strings.Contains(low, "permission denied"):
		return conv.Fail("没有权限写入输出文件夹，请换一个输出位置", detail)
	case strings.Contains(low, "cannot allocate memory") || strings.Contains(low, "memory allocation failed"):
		return conv.Fail("内存不够了，请关掉一些程序后再试", detail)
	}
	return conv.Fail(msg, detail)
}

// noRetry 表示这种错误换个办法再试也没用（比如磁盘满了、用户取消）
func noRetry(err error) bool {
	if isCancel(err) {
		return true
	}
	var ue *conv.UserError
	if errors.As(err, &ue) {
		return true
	}
	low := strings.ToLower(toolDetail(err))
	return strings.Contains(low, "no space left") || strings.Contains(low, "permission denied")
}

// ---------------------------------------------------------------- 文件

// moveFile 把 src 移到 dst（跨磁盘时复制再删除）
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	in.Close()
	os.Remove(src)
	return nil
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// safeInput 文件名里有 ImageMagick 会特殊处理的字符（[ ] % @ 等）时，在临时文件夹里做个链接或副本，
// 返回可以放心交给外部程序的路径。
func safeInput(path, tmp string) (string, error) {
	if !strings.ContainsAny(filepath.Base(path), "[]%@~{}*?") {
		return path, nil
	}
	f, err := os.CreateTemp(tmp, "input-*"+strings.ToLower(filepath.Ext(path)))
	if err != nil {
		return "", err
	}
	dst := f.Name()
	f.Close()
	os.Remove(dst)
	if err := os.Link(path, dst); err == nil {
		return dst, nil
	}
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
}

// ---------------------------------------------------------------- 文字

// humanSize 和 conv.HumanSize 一样，但去掉没用的小数（500 KB 而不是 500.0 KB）
func humanSize(n int64) string {
	s := conv.HumanSize(n)
	num, unit, ok := strings.Cut(s, " ")
	if ok && strings.Contains(num, ".") {
		num = strings.TrimRight(strings.TrimRight(num, "0"), ".")
	}
	return num + " " + unit
}

// clock 把秒数写成 1:23 或 1:02:03
func clock(sec float64) string {
	t := int(math.Round(sec))
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// secs 写成 ffmpeg 认的秒数
func secs(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// kbps 写成 ffmpeg 的码率参数
func kbps(bitsPerSec float64) string {
	return strconv.Itoa(max(1, int(bitsPerSec/1000))) + "k"
}

// trimRange 读开始、结束时间，检查是否合理。dur 是文件总长（0 = 不知道）。
// 返回的 start、end 为 -1 表示没设置；length 是截取后的长度（不知道时为 0）。
func trimRange(opt conv.Options, dur float64) (start, end, length float64, err error) {
	start, end = opt.Seconds(conv.OptStart), opt.Seconds(conv.OptEnd)
	if start == 0 {
		start = -1
	}
	if end == 0 {
		end = -1 // 结束时间填 0 当作没填
	}
	if dur > 0 {
		if start >= dur {
			return 0, 0, 0, conv.Fail(fmt.Sprintf("开始时间超过了文件长度（只有 %s）", clock(dur)), "")
		}
		if end >= dur {
			end = -1
		}
	}
	if end >= 0 && end <= max(start, 0) {
		return 0, 0, 0, conv.Fail("结束时间要比开始时间晚", "")
	}
	length = dur
	if end >= 0 {
		length = end
	}
	if start > 0 && length > 0 {
		length -= start
	}
	return start, end, length, nil
}

// isCancel 判断是不是用户取消（包括外部程序还没启动 ctx 就已经取消的情况）
func isCancel(err error) bool {
	return errors.Is(err, conv.ErrCancelled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
