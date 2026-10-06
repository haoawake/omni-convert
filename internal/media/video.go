package media

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// vfmt 描述一种输出视频格式
type vfmt struct {
	name    string
	ext     string
	muxer   string
	acodec  string   // 声音编码：aac mp3 opus wmav2 mp2；空 = 不要声音（gif、webp）
	akbps   int      // 默认声音码率
	acopyOK []string // 原来的声音是这些编码时可以直接复制，不用重新编码
	mov     bool     // mp4、mov 一家：加 faststart，H.265 用 hvc1 标签（苹果设备才认）
}

var videoFormats = map[string]vfmt{
	"mp4":  {"mp4", ".mp4", "mp4", "aac", 192, []string{"aac"}, true},
	"m4v":  {"m4v", ".m4v", "mp4", "aac", 192, []string{"aac"}, true},
	"mov":  {"mov", ".mov", "mov", "aac", 192, []string{"aac"}, true},
	"mkv":  {"mkv", ".mkv", "matroska", "aac", 192, []string{"aac", "mp3", "opus", "vorbis", "flac", "ac3", "eac3", "dts"}, false},
	"webm": {"webm", ".webm", "webm", "opus", 128, []string{"opus", "vorbis"}, false},
	"flv":  {"flv", ".flv", "flv", "aac", 192, []string{"aac", "mp3"}, false},
	"ts":   {"ts", ".ts", "mpegts", "aac", 192, []string{"aac", "mp3", "ac3"}, false},
	"avi":  {"avi", ".avi", "avi", "mp3", 192, []string{"mp3"}, false},
	"wmv":  {"wmv", ".wmv", "asf", "wmav2", 192, []string{"wmav2"}, false},
	"mpg":  {"mpg", ".mpg", "vob", "mp2", 224, []string{"mp2"}, false},
	"3gp":  {"3gp", ".3gp", "3gp", "aac", 128, []string{"aac"}, true},
	"gif":  {"gif", ".gif", "gif", "", 0, nil, false},
	"webp": {"webp", ".webp", "webp", "", 0, nil, false},
}

// sameVideoExt：选「保持原格式」时，扩展名对应的格式。不在表里的（rmvb、ogv、mxf……）改存成 MP4。
var sameVideoExt = map[string]string{
	".mp4": "mp4", ".m4v": "m4v", ".mov": "mov", ".qt": "mov", ".mkv": "mkv", ".webm": "webm",
	".flv": "flv", ".f4v": "mp4", ".ts": "ts", ".mts": "ts", ".m2ts": "ts", ".m2t": "ts",
	".avi": "avi", ".divx": "avi", ".xvid": "avi", ".wmv": "wmv", ".asf": "wmv",
	".mpg": "mpg", ".mpeg": "mpg", ".mpe": "mpg", ".vob": "mpg", ".3gp": "3gp", ".3g2": "3gp",
}

// ffprobe 里的编码名
var ffCodecName = map[string]string{
	"h264": "h264", "h265": "hevc", "av1": "av1", "vp9": "vp9", "mpeg4": "mpeg4", "wmv2": "wmv2", "mpeg2": "mpeg2video",
}

// 按质量编码时的参数：高、中、低
var (
	cpuCRF = map[string][3]int{
		"h264": {18, 23, 28}, "h265": {20, 26, 30}, "av1": {28, 35, 42}, "vp9": {28, 33, 40},
		"mpeg4": {2, 4, 7}, "wmv2": {2, 4, 7}, "mpeg2": {2, 4, 7}, // 这三个是 -q:v
	}
	gpuCQ = map[string][3]int{"h264": {20, 25, 30}, "h265": {22, 27, 32}, "av1": {28, 34, 40}}
	// VideoToolbox 的质量是 1~100，越大越清晰
	vtQuality = map[string][3]int{"h264": {70, 60, 50}, "h265": {66, 56, 46}}
	// 每像素每帧至少要这么多比特才不会糊成一片；按大小压缩时低于这个值就自动降低分辨率
	minBPP = map[string]float64{"h264": 0.035, "h265": 0.025, "av1": 0.022, "vp9": 0.025, "mpeg4": 0.07, "wmv2": 0.08, "mpeg2": 0.08}
)

// 高清视频常用的短边像素，自动降分辨率时从这里挑
var shortLadder = []int{2160, 1440, 1080, 720, 540, 480, 360, 270, 240, 180, 144}

// 转 GIF 时，预计要攒在内存里的画面超过这么多字节就分两步做（先生成调色板再上色）
var gifBufferLimit = 1.5e9

// HDR 转普通画面（PQ、HLG 的视频直接转 8 位会发灰）
const tonemapChain = "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"

// vplan 是算好的一次视频转换
type vplan struct {
	j        *conv.Job
	in       string
	info     *Info
	f        vfmt
	muxer    string
	muxArgs  []string
	codec    string // h264 h265 av1 vp9 mpeg4 wmv2 mpeg2 gif webp
	qi       int    // 0 高 1 中 2 低
	start    float64
	end      float64
	length   float64 // 输出时长（秒），0 = 不知道
	audio    bool    // 输出里有声音
	fps      string  // fps 滤镜参数，空 = 保持
	outFPS   float64
	geo      geometry
	padColor string
	fixed    bool       // 用户固定了输出尺寸
	resize   resizeSpec // 用户的尺寸设置
	tonemap  bool
	vcopy    bool // 画面直接复制
	acopy    bool // 声音直接复制
	gpu      string
	target   int64
	vbit     float64 // 按大小压缩时的画面码率（bit/s）
	abit     float64 // 按大小压缩时的声音码率
	out      string
	tmp      string
	note     string // 进度旁边的说明
}

// Video 返回把视频转成 format 的转换器。
// format："same" 或 mp4 mkv mov avi webm flv wmv m4v ts mpg 3gp gif webp（webp 是动图）。
func Video(format string) conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		return convertVideo(ctx, j, strings.ToLower(format))
	}
}

func convertVideo(ctx context.Context, j *conv.Job, format string) error {
	if err := tools.Need(tools.FFmpeg()); err != nil {
		return err
	}
	j.Report(-1, "正在读取视频信息")
	info, err := Probe(ctx, j.Input())
	if err != nil {
		return err
	}
	if !info.HasVideo {
		return conv.Fail("这个文件里没有画面", "如果只想要声音，请到左边的「音频」页")
	}
	opt := j.Opt
	p := &vplan{j: j, in: j.Input(), info: info}

	ext := conv.Ext(j.Input())
	if format == "same" {
		name, ok := sameVideoExt[ext]
		if !ok {
			name, ext = "mp4", ".mp4"
		}
		p.f = videoFormats[name]
		p.f.ext = ext
	} else {
		var ok bool
		if p.f, ok = videoFormats[format]; !ok {
			return conv.Fail("不支持转成 "+format+" 格式", "")
		}
	}
	p.muxer = p.f.muxer
	switch p.f.ext {
	case ".m2ts", ".mts":
		p.muxArgs = []string{"-mpegts_m2ts_mode", "1"}
	case ".3g2":
		p.muxer = "3g2"
	}
	if p.f.mov {
		p.muxArgs = append(p.muxArgs, "-movflags", "+faststart")
	}

	want := strings.ToLower(opt.Str(conv.OptVCodec, "h264"))
	switch want {
	case "hevc", "h.265", "x265":
		want = "h265"
	case "h264", "h265", "av1":
	default:
		want = "h264"
	}
	p.codec = pickVCodec(p.f.name, want)
	switch strings.ToLower(opt.Str(conv.OptQuality, "medium")) {
	case "high", "高":
		p.qi = 0
	case "low", "低":
		p.qi = 2
	default:
		p.qi = 1
	}

	if p.start, p.end, p.length, err = trimRange(opt, info.Duration); err != nil {
		return err
	}
	p.audio = info.HasAudio && p.f.acodec != "" && !opt.Bool(conv.OptMute)
	p.target = max(opt.Int64(conv.OptTargetSize, 0), 0)
	anim := p.codec == "gif" || p.codec == "webp"
	even := !anim

	// 帧率
	srcFPS := info.FPS
	fps := opt.Float(conv.OptFPS, 0)
	if fps < 0 || fps > 240 {
		fps = 0
	}
	switch p.codec {
	case "gif":
		if fps <= 0 {
			fps = 12
			if srcFPS > 0 {
				fps = min(fps, srcFPS)
			}
		}
	case "webp":
		if fps <= 0 {
			fps = 15
			if srcFPS > 0 {
				fps = min(fps, srcFPS)
			}
		}
	case "mpeg2": // MPEG-2 只认标准帧率
		fps = nearestStdRate(firstPositive(fps, srcFPS, 25), true)
	case "mpeg4", "wmv2": // 老格式要固定帧率，而且分母不能太大
		fps = nearestStdRate(firstPositive(fps, srcFPS, 25), false)
	}
	p.outFPS = firstPositive(fps, srcFPS, 30)
	if fps > 0 && (math.Abs(fps-srcFPS) > 0.01 || p.codec == "mpeg2" || p.codec == "mpeg4" || p.codec == "wmv2") {
		p.fps = rateString(fps)
	}

	// 尺寸
	r := parseResize(opt)
	p.fixed = r.fixedBox()
	p.padColor = "black"
	if r.BG != "" && r.BG != "none" {
		p.padColor = r.BG
	}
	if anim && !r.active() {
		limit := 480
		if p.codec == "webp" {
			limit = 640
		}
		if info.Width > limit {
			r = resizeSpec{Mode: "box", W: limit, Fit: "contain"}
		}
	}
	p.geo = p.baseGeometry(r, even)

	p.tonemap = (info.ColorTRC == "smpte2084" || info.ColorTRC == "arib-std-b67") && !strings.Contains(info.PixFmt, "rgb")

	if p.tmp, err = j.TempDir(); err != nil {
		return err
	}
	p.out = j.OutFile(p.f.ext)

	if anim {
		return p.runAnim(ctx)
	}

	// 能直接复制画面（比如 MKV 转 MP4、编码本来就对）就不重新编码：又快又不损失画质。
	// 选了「低画质」，或者中画质但原视频码率特别高（相机原片）时，还是重新编码，好把文件变小。
	srcBPP := 0.0
	if info.Bitrate > 0 && info.FPS > 0 {
		srcBPP = float64(info.Bitrate) / (float64(info.codedW*info.codedH) * info.FPS)
	}
	copyOK := !r.active() && opt.Float(conv.OptFPS, 0) <= 0 && p.start <= 0 && p.end < 0 && p.fps == "" &&
		(p.qi == 0 || p.qi == 1 && srcBPP < 0.3) &&
		ffCodecName[p.codec] == info.VideoCodec && copyPixOK(p.codec, info.PixFmt) && (info.Rotation == 0 || p.f.mov)
	// 按大小压缩时，原文件本来就不超过目标就直接复制
	p.vcopy = copyOK && (p.target == 0 || fileSize(p.in) <= p.target*97/100)
	p.acopy = p.audio && p.start <= 0 && p.end < 0 && (p.target == 0 || p.vcopy) &&
		slices.Contains(p.f.acopyOK, info.AudioCodec) && (info.Channels <= 2 || p.f.acodec == "aac" || p.f.name == "mkv")
	p.resize = r

	// 按大小压缩：算码率
	if p.target > 0 && !p.vcopy {
		if err := p.planBitrate(); err != nil {
			return err
		}
	}

	if opt.Bool(conv.OptGPU) && (p.codec == "h264" || p.codec == "h265" || p.codec == "av1") {
		p.gpu = GPUEncoders(ctx)[p.codec]
	}

	for attempt := 0; ; {
		copied := p.vcopy
		if err := p.encode(ctx); err != nil {
			return err
		}
		if p.target == 0 {
			return nil
		}
		sz := fileSize(p.out)
		if sz <= p.target {
			return nil
		}
		if copied {
			// 直接复制的结果比目标大（封装格式的开销），改成重新编码
			p.vcopy, p.acopy = false, false
			if p.vbit == 0 {
				if err := p.planBitrate(); err != nil {
					return err
				}
			}
			continue
		}
		attempt++
		if attempt > 2 || ctx.Err() != nil {
			return conv.Fail(fmt.Sprintf("压不到 %s，结果是 %s", humanSize(p.target), humanSize(sz)), "可以把文件大小放宽一些，或者把分辨率调小")
		}
		// 比目标大：按比例降低码率再压一次
		p.vbit *= float64(p.target) / float64(sz) * 0.95
		if p.vbit < 50000 {
			return conv.Fail(fmt.Sprintf("压不到 %s，结果是 %s", humanSize(p.target), humanSize(sz)), "可以把文件大小放宽一些，或者把分辨率调小")
		}
		p.note = "结果比目标大了一点，正在重新压缩"
	}
}

// pickVCodec 根据容器和用户选的编码决定实际用的编码
func pickVCodec(format, want string) string {
	switch format {
	case "mp4", "m4v", "mov", "mkv":
		return want
	case "flv", "ts":
		if want == "av1" {
			return "h264"
		}
		return want
	case "3gp":
		return "h264"
	case "webm":
		if want == "av1" {
			return "av1"
		}
		return "vp9"
	case "avi":
		return "mpeg4"
	case "wmv":
		return "wmv2"
	case "mpg":
		return "mpeg2"
	}
	return format // gif、webp
}

func copyPixOK(codec, pix string) bool {
	switch pix {
	case "yuv420p", "yuvj420p":
		return true
	case "yuv420p10le":
		return codec == "h265" || codec == "av1" || codec == "vp9"
	}
	return false
}

// firstPositive 返回第一个大于 0 的值
func firstPositive(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// nearestStdRate 挑最接近的标准帧率。strict 为 true 时只能是 MPEG-2 认的那几个；
// 否则差得远时取整数帧率。
func nearestStdRate(fps float64, strict bool) float64 {
	std := []float64{24000.0 / 1001, 24, 25, 30000.0 / 1001, 30, 50, 60000.0 / 1001, 60}
	best := std[0]
	for _, s := range std {
		if math.Abs(s-fps) < math.Abs(best-fps) {
			best = s
		}
	}
	if strict || math.Abs(best-fps)/fps < 0.005 {
		return best
	}
	return min(max(math.Round(fps), 1), 60)
}

// rateString 把帧率写成 ffmpeg 认的形式，29.97 写成 30000/1001
func rateString(fps float64) string {
	for _, n := range []int{24000, 30000, 48000, 60000, 120000} {
		if math.Abs(fps-float64(n)/1001) < 0.001 {
			return strconv.Itoa(n) + "/1001"
		}
	}
	return strconv.FormatFloat(fps, 'f', -1, 64)
}

// baseGeometry 算出画面尺寸的处理步骤
func (p *vplan) baseGeometry(r resizeSpec, even bool) geometry {
	info := p.info
	if r.active() {
		return r.plan(info.Width, info.Height, even)
	}
	if info.Width != info.codedW || info.Height != info.codedH {
		// 非方形像素：缩放成方形像素
		return resizeSpec{Mode: "box", W: info.Width, H: info.Height, Fit: "stretch"}.plan(info.Width, info.Height, even)
	}
	g := geometry{SW: info.codedW, SH: info.codedH}
	if even && (g.SW%2 != 0 || g.SH%2 != 0) {
		// 宽高是奇数时 yuv420p 编不了，裁掉一像素
		g.CW, g.CH = max(2, g.SW&^1), max(2, g.SH&^1)
	}
	return g
}

// planBitrate 按目标大小算出画面、声音码率；码率太低时自动降低分辨率
func (p *vplan) planBitrate() error {
	r := p.resize
	if p.length <= 0 {
		return conv.Fail("读不出视频时长，没法按文件大小压缩", "")
	}
	total := float64(p.target) * 8 * 0.97 / p.length // 留 3% 给封装格式
	a := 0.0
	if p.audio {
		a = min(max(total*0.12, 48000), 128000)
		if p.f.acodec == "mp2" {
			a = snapDown(a/1000, mp2Rates) * 1000
		}
	}
	v := total - a
	if v < 60000 {
		need := int64((60000 + a) * p.length / 8 / 0.97)
		return conv.Fail(fmt.Sprintf("视频太长，压不到 %s（这段视频至少要 %s 左右）", humanSize(p.target), humanSize(need)),
			"可以先截取一段，或者把文件大小放宽一些")
	}
	// 码率没必要比原视频还高（那样文件只会变大，画质也不会更好）
	if src := float64(p.info.Bitrate - p.info.AudioBitrate); p.info.Bitrate > 0 && src > 60000 && v > src && !r.active() && p.fps == "" {
		v = src
	}
	p.vbit, p.abit = v, a

	if r.active() {
		return nil // 用户自己定了尺寸，不动
	}
	w, h := p.geo.Out()
	bpp := v / (float64(w*h) * p.outFPS)
	th := minBPP[p.codec]
	if th == 0 || bpp >= th {
		return nil
	}
	short := min(w, h)
	want := float64(short) * math.Sqrt(bpp/th)
	ns := 0
	for _, s := range shortLadder {
		if float64(s) <= want && s < short {
			ns = s
			break
		}
	}
	if ns == 0 {
		ns = min(144, short)
	}
	if ns >= short {
		return nil
	}
	rr := resizeSpec{Mode: "box", Fit: "contain"}
	if p.info.Width >= p.info.Height {
		rr.H = ns
	} else {
		rr.W = ns
	}
	p.geo = rr.plan(p.info.Width, p.info.Height, true)
	nw, nh := p.geo.Out()
	p.note = fmt.Sprintf("为了压到 %s，分辨率降到 %d×%d", humanSize(p.target), nw, nh)
	return nil
}

var mp2Rates = []float64{32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384}

// snapDown 在 list 里取不超过 v 的最大值（v 比最小的还小时取最小的）
func snapDown(v float64, list []float64) float64 {
	best := list[0]
	for _, x := range list {
		if x <= v {
			best = x
		}
	}
	return best
}

// vf 是画面滤镜
func (p *vplan) vf(tonemap bool) string {
	var f []string
	if p.fps != "" {
		f = append(f, "fps="+p.fps)
	}
	if tonemap {
		f = append(f, tonemapChain)
	}
	g := p.geo
	if g.SW != p.info.codedW || g.SH != p.info.codedH {
		f = append(f, fmt.Sprintf("scale=%d:%d", g.SW, g.SH), "setsar=1")
	}
	if g.CW > 0 {
		f = append(f, fmt.Sprintf("crop=%d:%d", g.CW, g.CH))
	}
	if g.PW > 0 {
		f = append(f, fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s", g.PW, g.PH, p.padColor))
	}
	return strings.Join(f, ",")
}

// inputArgs 是输入和截取部分的参数
func (p *vplan) inputArgs() []string {
	var a []string
	if p.start > 0 {
		a = append(a, "-ss", secs(p.start))
	}
	a = append(a, "-i", ffPath(p.in))
	if p.end > 0 {
		a = append(a, "-t", secs(p.end-max(p.start, 0)))
	}
	return a
}

// args 生成一次 ffmpeg 的参数。pass：0 = 一遍编码，1、2 = 两遍编码的第几遍。
func (p *vplan) args(pass int, gpu string, vcopy, acopy, tonemap bool) []string {
	a := p.inputArgs()
	a = append(a, "-map", fmt.Sprintf("0:%d", p.info.VideoIndex))
	withAudio := p.audio && pass != 1
	if withAudio {
		a = append(a, "-map", fmt.Sprintf("0:%d", p.info.AudioIndex))
	}
	a = append(a, "-sn", "-dn", "-map_metadata", "0")
	if p.start > 0 || p.end > 0 {
		a = append(a, "-map_chapters", "-1")
	}
	if vcopy {
		a = append(a, "-c:v", "copy")
		if p.codec == "h265" && p.f.mov {
			a = append(a, "-tag:v", "hvc1")
		}
	} else {
		if vf := p.vf(tonemap); vf != "" {
			a = append(a, "-vf", vf)
		}
		if gpu != "" {
			a = append(a, gpuVideoArgs(gpu, p.codec, p.qi, p.vbit, p.f.mov)...)
		} else {
			a = append(a, cpuVideoArgs(p.codec, p.qi, p.vbit, p.f.mov)...)
		}
		if pass > 0 {
			switch p.codec {
			case "h264":
				a = append(a, "-pass", strconv.Itoa(pass), "-passlogfile", "x264pass")
			case "h265":
				a = append(a, "-pass", strconv.Itoa(pass), "-x265-stats", "x265pass.log")
			}
		}
	}
	switch {
	case !withAudio:
		a = append(a, "-an")
	case acopy:
		a = append(a, "-c:a", "copy")
	default:
		a = append(a, videoAudioArgs(p.f, p.info, p.abit)...)
	}
	if pass == 1 {
		return append(a, "-f", "null", "NUL")
	}
	a = append(a, "-f", p.muxer)
	a = append(a, p.muxArgs...)
	return append(a, ffPath(p.out))
}

// cpuVideoArgs 是 CPU 编码参数。bitrate > 0 时按码率编码（压缩到指定大小），否则按质量。
func cpuVideoArgs(codec string, qi int, bitrate float64, mov bool) []string {
	q := strconv.Itoa(cpuCRF[codec][qi])
	var a []string
	rate := func(cap bool) {
		a = append(a, "-b:v", kbps(bitrate))
		if cap {
			a = append(a, "-maxrate", kbps(bitrate*1.5), "-bufsize", kbps(bitrate*2))
		}
	}
	switch codec {
	case "h264":
		a = []string{"-c:v", "libx264", "-preset", "medium", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			rate(false)
		} else {
			a = append(a, "-crf", q)
		}
	case "h265":
		a = []string{"-c:v", "libx265", "-preset", "medium", "-pix_fmt", "yuv420p", "-x265-params", "log-level=error"}
		if mov {
			a = append(a, "-tag:v", "hvc1")
		}
		if bitrate > 0 {
			rate(false)
		} else {
			a = append(a, "-crf", q)
		}
	case "av1":
		a = []string{"-c:v", "libsvtav1", "-preset", "8", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			rate(false)
		} else {
			a = append(a, "-crf", q)
		}
	case "vp9":
		a = []string{"-c:v", "libvpx-vp9", "-row-mt", "1", "-deadline", "good", "-cpu-used", "4", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			rate(true)
		} else {
			a = append(a, "-crf", q, "-b:v", "0")
		}
	case "mpeg4":
		a = []string{"-c:v", "mpeg4", "-vtag", "XVID", "-pix_fmt", "yuv420p", "-mbd", "rd", "-flags", "+mv4+aic", "-trellis", "2", "-cmp", "2", "-subcmp", "2", "-g", "300"}
		if bitrate > 0 {
			rate(true)
		} else {
			a = append(a, "-q:v", q)
		}
	case "wmv2":
		a = []string{"-c:v", "wmv2", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			rate(true)
		} else {
			a = append(a, "-q:v", q)
		}
	case "mpeg2":
		a = []string{"-c:v", "mpeg2video", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			rate(true)
		} else {
			a = append(a, "-q:v", q)
		}
	}
	return a
}

// gpuVideoArgs 是显卡编码参数（NVIDIA NVENC、AMD AMF、Intel QSV、苹果 VideoToolbox）
func gpuVideoArgs(enc, codec string, qi int, bitrate float64, mov bool) []string {
	a := []string{"-c:v", enc, "-pix_fmt", "nv12"}
	q := strconv.Itoa(gpuCQ[codec][qi])
	capped := func() []string {
		return []string{"-b:v", kbps(bitrate), "-maxrate", kbps(bitrate * 1.5), "-bufsize", kbps(bitrate * 2)}
	}
	switch {
	case strings.HasSuffix(enc, "_nvenc"):
		a = append(a, "-preset", "p5", "-tune", "hq", "-rc", "vbr")
		if bitrate > 0 {
			a = append(a, "-multipass", "fullres")
			a = append(a, capped()...)
		} else {
			a = append(a, "-cq", q, "-b:v", "0")
		}
	case strings.HasSuffix(enc, "_amf"):
		a = append(a, "-quality", "balanced")
		if bitrate > 0 {
			a = append(a, "-rc", "vbr_peak")
			a = append(a, capped()...)
		} else {
			a = append(a, "-rc", "qvbr", "-qvbr_quality_level", q)
		}
	case strings.HasSuffix(enc, "_qsv"):
		a = append(a, "-preset", "medium")
		if bitrate > 0 {
			a = append(a, capped()...)
		} else {
			a = append(a, "-global_quality", q)
		}
	case strings.HasSuffix(enc, "_videotoolbox"):
		if bitrate > 0 {
			a = append(a, capped()...)
		} else {
			a = append(a, "-q:v", strconv.Itoa(vtQuality[codec][qi]))
		}
	}
	if codec == "h265" && mov {
		a = append(a, "-tag:v", "hvc1")
	}
	return a
}

// videoAudioArgs 是视频里声音的编码参数。bitrate > 0 时用这个码率，否则用格式的默认码率。
func videoAudioArgs(f vfmt, info *Info, bitrate float64) []string {
	br := bitrate
	if br <= 0 {
		br = float64(f.akbps) * 1000
	}
	var a []string
	switch f.acodec {
	case "aac":
		a = []string{"-c:a", "aac", "-b:a", kbps(br)}
	case "mp3":
		a = []string{"-c:a", "libmp3lame", "-b:a", kbps(br)}
	case "opus":
		a = []string{"-c:a", "libopus", "-b:a", kbps(br)}
	case "wmav2":
		a = []string{"-c:a", "wmav2", "-b:a", kbps(br)}
	case "mp2":
		a = []string{"-c:a", "mp2", "-b:a", kbps(snapDown(br/1000, mp2Rates) * 1000), "-ar", "48000"}
	}
	if info.Channels > 2 && f.acodec != "aac" {
		a = append(a, "-ac", "2")
	}
	if info.SampleRate > 48000 && (f.acodec == "mp3" || f.acodec == "wmav2") {
		a = append(a, "-ar", "48000")
	}
	return a
}

// report 汇报进度，frac 在 [from, from+span] 之间
func (p *vplan) report(from, span float64, note string) func(float64) {
	if p.note != "" {
		if note != "" {
			note = p.note + "，" + note
		} else {
			note = p.note
		}
	}
	if p.length > 0 {
		p.j.Report(from, note)
	} else {
		p.j.Report(-1, note) // 不知道时长，进度条转圈
	}
	return func(f float64) {
		if p.length > 0 {
			p.j.Report(from+f*span, note)
		}
	}
}

// encode 编码一次：能复制就复制；选了显卡就先用显卡，失败了换 CPU；
// 复制声音、HDR 转换出问题时再用最保守的参数试一次。
func (p *vplan) encode(ctx context.Context) error {
	if p.vcopy {
		err := runFFmpeg(ctx, p.args(0, "", true, p.acopy, false), p.tmp, p.length, p.report(0, 1, "原画质快速转换"))
		if err == nil || noRetry(err) {
			return friendly(err, "视频转换失败")
		}
		p.vcopy, p.acopy = false, false // 复制不行就重新编码
		if p.target > 0 && p.vbit == 0 {
			if err := p.planBitrate(); err != nil {
				return err
			}
		}
	}
	if p.gpu != "" {
		err := p.runEncode(ctx, p.gpu, p.acopy, p.tonemap, "用显卡编码")
		if err == nil || noRetry(err) {
			return friendly(err, "视频转换失败")
		}
		p.gpu = "" // 显卡编码失败，后面都用 CPU
		note := "显卡编码没成功，改用 CPU 编码"
		err = p.runEncode(ctx, "", p.acopy, p.tonemap, note)
		if err != nil && !noRetry(err) && (p.acopy || p.tonemap) {
			p.acopy, p.tonemap = false, false
			err = p.runEncode(ctx, "", false, false, note)
		}
		return friendly(err, "视频转换失败")
	}
	err := p.runEncode(ctx, "", p.acopy, p.tonemap, "")
	if err != nil && !noRetry(err) && (p.acopy || p.tonemap) {
		p.acopy, p.tonemap = false, false
		err = p.runEncode(ctx, "", false, false, "")
	}
	return friendly(err, "视频转换失败")
}

// runEncode 重新编码。按大小压缩、用 x264 / x265 时分两遍编码，码率更准。
func (p *vplan) runEncode(ctx context.Context, gpu string, acopy, tonemap bool, note string) error {
	if p.target > 0 && gpu == "" && (p.codec == "h264" || p.codec == "h265") {
		n1, n2 := "第 1 遍（共 2 遍）", "第 2 遍（共 2 遍）"
		if err := runFFmpeg(ctx, p.args(1, "", false, false, tonemap), p.tmp, p.length, p.report(0, 0.5, n1)); err != nil {
			return err
		}
		return runFFmpeg(ctx, p.args(2, "", false, acopy, tonemap), p.tmp, p.length, p.report(0.5, 0.5, n2))
	}
	return runFFmpeg(ctx, p.args(0, gpu, false, acopy, tonemap), p.tmp, p.length, p.report(0, 1, note))
}

// ---------------------------------------------------------------- 动图

// runAnim 转成 GIF 或动态 WebP。指定了大小时，太大就缩小画面再来，最多试 5 次。
func (p *vplan) runAnim(ctx context.Context) error {
	tonemap := p.tonemap
	for attempt := 0; ; attempt++ {
		err := p.encodeAnim(ctx, tonemap)
		if err != nil && tonemap && !noRetry(err) {
			tonemap = false
			err = p.encodeAnim(ctx, false)
		}
		if err != nil {
			return friendly(err, "动图转换失败")
		}
		if p.target <= 0 {
			return nil
		}
		sz := fileSize(p.out)
		if sz <= p.target {
			return nil
		}
		w, h := p.geo.Out()
		if p.fixed || attempt >= 4 || max(w, h) <= 32 {
			msg := fmt.Sprintf("压不到 %s，结果是 %s", humanSize(p.target), humanSize(sz))
			if p.fixed {
				return conv.Fail(msg, fmt.Sprintf("尺寸固定为 %d×%d，可以把尺寸改小、截短一些或者降低帧率", w, h))
			}
			return conv.Fail(msg, "可以截短一些，或者降低帧率")
		}
		// 大小大致和像素数成正比
		s := math.Sqrt(float64(p.target)/float64(sz)) * 0.93
		nw, nh := max(16, int(math.Round(float64(w)*s))), max(16, int(math.Round(float64(h)*s)))
		p.geo = resizeSpec{Mode: "box", W: nw, H: nh, Fit: "stretch"}.plan(p.info.Width, p.info.Height, false)
		p.note = fmt.Sprintf("为了压到 %s，尺寸缩小到 %d×%d", humanSize(p.target), nw, nh)
	}
}

func (p *vplan) encodeAnim(ctx context.Context, tonemap bool) error {
	vf := p.vf(tonemap)
	if vf == "" {
		vf = "null"
	}
	if p.codec == "webp" {
		q := []string{"85", "75", "60"}[p.qi]
		a := p.inputArgs()
		a = append(a, "-map", fmt.Sprintf("0:%d", p.info.VideoIndex), "-an", "-sn", "-dn", "-vf", vf,
			"-c:v", "libwebp_anim", "-lossless", "0", "-quality", q, "-compression_level", "4", "-pix_fmt", "yuv420p",
			"-loop", "0", "-f", "webp", ffPath(p.out))
		return runFFmpeg(ctx, a, p.tmp, p.length, p.report(0, 1, ""))
	}

	// GIF：先统计颜色生成调色板，再按调色板上色。片子很长时分两步做，免得把所有画面都攒在内存里。
	const paletteUse = "paletteuse=dither=sierra2_4a:diff_mode=rectangle"
	w, h := p.geo.Out()
	frames := p.length * p.outFPS
	if p.length <= 0 || frames*float64(w*h)*4 > gifBufferLimit {
		pal := filepath.Join(p.tmp, "palette.png")
		a := p.inputArgs()
		a = append(a, "-map", fmt.Sprintf("0:%d", p.info.VideoIndex), "-an", "-sn", "-dn",
			"-vf", vf+",palettegen=stats_mode=diff", "-update", "1", "-f", "image2", ffPath(pal))
		if err := runFFmpeg(ctx, a, p.tmp, p.length, p.report(0, 0.3, "生成调色板")); err != nil {
			return err
		}
		a = p.inputArgs()
		a = append(a, "-i", ffPath(pal),
			"-filter_complex", fmt.Sprintf("[0:%d]%s[x];[x][1:v]%s[out]", p.info.VideoIndex, vf, paletteUse), "-map", "[out]",
			"-an", "-sn", "-dn", "-loop", "0", "-f", "gif", ffPath(p.out))
		return runFFmpeg(ctx, a, p.tmp, p.length, p.report(0.3, 0.7, ""))
	}
	a := p.inputArgs()
	a = append(a, "-map", fmt.Sprintf("0:%d", p.info.VideoIndex), "-an", "-sn", "-dn",
		"-vf", vf+",split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]"+paletteUse,
		"-loop", "0", "-f", "gif", ffPath(p.out))
	return runFFmpeg(ctx, a, p.tmp, p.length, p.report(0, 1, ""))
}
