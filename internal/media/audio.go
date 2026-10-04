package media

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// afmt 描述一种输出音频格式
type afmt struct {
	name     string
	ext      string
	muxer    string
	codec    string    // ffmpeg 编码器
	copyFrom []string  // 原来就是这些编码时可以直接复制（ffprobe 的编码名）
	defKbps  int       // 默认码率；0 = 无损或固定码率
	minKbps  int       // 码率范围（按大小压缩时用）
	maxKbps  int       //
	rates    []float64 // 只能用这几种码率（kbps），nil = 范围内都行
	lossless bool
	maxCh    int   // 最多几个声道，0 = 不限
	srates   []int // 只支持这些采样率，nil = 不限
	extra    []string
	cover    bool // 能带专辑封面
}

var (
	mp3Rates  = []float64{32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	ac3Rates  = []float64{32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512, 576, 640}
	amrRates  = []float64{4.75, 5.15, 5.9, 6.7, 7.4, 7.95, 10.2, 12.2}
	mp3SRates = []int{8000, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000}
	aacSRates = []int{8000, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000, 64000, 88200, 96000}
)

var audioFormats = map[string]afmt{
	"mp3":  {name: "mp3", ext: ".mp3", muxer: "mp3", codec: "libmp3lame", copyFrom: []string{"mp3"}, defKbps: 192, minKbps: 32, maxKbps: 320, rates: mp3Rates, maxCh: 2, srates: mp3SRates, extra: []string{"-id3v2_version", "3"}, cover: true},
	"m4a":  {name: "m4a", ext: ".m4a", muxer: "ipod", codec: "aac", copyFrom: []string{"aac"}, defKbps: 192, minKbps: 16, maxKbps: 512, srates: aacSRates, extra: []string{"-movflags", "+faststart"}, cover: true},
	"m4r":  {name: "m4r", ext: ".m4r", muxer: "ipod", codec: "aac", copyFrom: []string{"aac"}, defKbps: 192, minKbps: 16, maxKbps: 512, srates: aacSRates, extra: []string{"-movflags", "+faststart"}, cover: true},
	"aac":  {name: "aac", ext: ".aac", muxer: "adts", codec: "aac", copyFrom: []string{"aac"}, defKbps: 192, minKbps: 16, maxKbps: 512, srates: aacSRates},
	"wav":  {name: "wav", ext: ".wav", muxer: "wav", codec: "pcm_s16le", copyFrom: []string{"pcm_s16le"}, lossless: true},
	"flac": {name: "flac", ext: ".flac", muxer: "flac", codec: "flac", copyFrom: []string{"flac"}, lossless: true, cover: true},
	"aiff": {name: "aiff", ext: ".aiff", muxer: "aiff", codec: "pcm_s16be", copyFrom: []string{"pcm_s16be"}, lossless: true},
	"ogg":  {name: "ogg", ext: ".ogg", muxer: "ogg", codec: "libvorbis", copyFrom: []string{"vorbis"}, defKbps: 192, minKbps: 48, maxKbps: 480},
	"opus": {name: "opus", ext: ".opus", muxer: "opus", codec: "libopus", copyFrom: []string{"opus"}, defKbps: 128, minKbps: 6, maxKbps: 510, srates: []int{8000, 12000, 16000, 24000, 48000}},
	"wma":  {name: "wma", ext: ".wma", muxer: "asf", codec: "wmav2", copyFrom: []string{"wmav2"}, defKbps: 160, minKbps: 32, maxKbps: 320, maxCh: 2, srates: []int{8000, 11025, 16000, 22050, 32000, 44100, 48000}},
	"ac3":  {name: "ac3", ext: ".ac3", muxer: "ac3", codec: "ac3", copyFrom: []string{"ac3"}, defKbps: 448, minKbps: 32, maxKbps: 640, rates: ac3Rates, maxCh: 6, srates: []int{32000, 44100, 48000}},
	"amr":  {name: "amr", ext: ".amr", muxer: "amr", codec: "libopencore_amrnb", copyFrom: []string{"amr_nb"}, minKbps: 4, maxKbps: 13, rates: amrRates, maxCh: 1, srates: []int{8000}},
}

// sameAudioExt：选「保持原格式」时，扩展名对应的格式；不在表里的（APE、DTS……）改存成 MP3
var sameAudioExt = map[string]string{
	".mp3": "mp3", ".m4a": "m4a", ".m4b": "m4a", ".m4r": "m4r", ".aac": "aac", ".wav": "wav", ".wave": "wav",
	".flac": "flac", ".aiff": "aiff", ".aif": "aiff", ".aifc": "aiff", ".ogg": "ogg", ".oga": "ogg",
	".opus": "opus", ".wma": "wma", ".ac3": "ac3", ".amr": "amr",
}

// iPhone 铃声最长 40 秒
const ringtoneMax = 40.0

// aplan 是算好的一次音频转换
type aplan struct {
	j         *conv.Job
	in        string
	info      *Info
	f         afmt
	start     float64
	end       float64
	length    float64
	kbps      float64 // 码率；0 = 不需要（无损、复制）
	ch        int     // 输出声道数，0 = 保持
	rate      int     // 输出采样率，0 = 保持
	normalize bool
	copy      bool // 直接复制，不重新编码
	cover     bool // 带上专辑封面
	target    int64
	out       string
	note      string
}

// Audio 返回把音频（或者视频里的声音）转成 format 的转换器。
// format："same" 或 mp3 m4a aac wav flac ogg opus wma aiff ac3 amr m4r（m4r 是 iPhone 铃声）。
func Audio(format string) conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		return convertAudio(ctx, j, strings.ToLower(format))
	}
}

func convertAudio(ctx context.Context, j *conv.Job, format string) error {
	if err := tools.Need(tools.FFmpeg()); err != nil {
		return err
	}
	j.Report(-1, "正在读取文件信息")
	info, err := Probe(ctx, j.Input())
	if err != nil {
		return err
	}
	if !info.HasAudio {
		return conv.Fail("这个文件里没有声音", "")
	}
	opt := j.Opt
	p := &aplan{j: j, in: j.Input(), info: info}

	if format == "same" {
		ext := conv.Ext(j.Input())
		if name, ok := sameAudioExt[ext]; ok {
			p.f = audioFormats[name]
			p.f.ext = ext
		} else {
			p.f = audioFormats["mp3"]
		}
	} else {
		var ok bool
		if p.f, ok = audioFormats[format]; !ok {
			return conv.Fail("不支持转成 "+format+" 格式", "")
		}
	}
	f := p.f

	if p.start, p.end, p.length, err = trimRange(opt, info.Duration); err != nil {
		return err
	}
	if f.name == "m4r" && p.end < 0 && p.length > ringtoneMax {
		p.end = max(p.start, 0) + ringtoneMax
		p.length = ringtoneMax
		p.note = "iPhone 铃声最长 40 秒，只保留前 40 秒"
		if p.start > 0 {
			p.note = fmt.Sprintf("iPhone 铃声最长 40 秒，只保留从 %s 开始的 40 秒", clock(p.start))
		}
	}

	// 声道、采样率
	p.ch = opt.Int(conv.OptChannels, 0)
	if p.ch != 1 && p.ch != 2 {
		p.ch = 0
	}
	if f.maxCh > 0 && (p.ch > f.maxCh || p.ch == 0 && info.Channels > f.maxCh) {
		p.ch = f.maxCh
	}
	p.rate = max(opt.Int(conv.OptRate, 0), 0)
	p.normalize = opt.Bool(conv.OptNormalize)
	if p.normalize && p.rate == 0 {
		p.rate = info.SampleRate // 音量标准化会把采样率变成 192 kHz，要改回来
		if p.rate <= 0 {
			p.rate = 48000
		}
	}
	if p.rate > 0 && f.srates != nil {
		p.rate = nearestInt(p.rate, f.srates)
	}
	if f.name == "amr" {
		p.rate, p.ch = 8000, 1
	}

	// 码率
	userKbps := opt.Float(conv.OptABitrate, 0)
	if !f.lossless {
		k := float64(f.defKbps)
		switch {
		case userKbps > 0:
			k = userKbps
		case format == "same" && slices.Contains(f.copyFrom, info.AudioCodec) && info.AudioBitrate > 0:
			k = float64(info.AudioBitrate) / 1000 // 保持原格式时沿用原来的码率，免得越转越大
		case f.name == "amr":
			k = 12.2
		}
		p.kbps = p.clampKbps(k, false)
	}

	p.target = max(opt.Int64(conv.OptTargetSize, 0), 0)
	if p.target > 0 {
		if err := p.planTarget(); err != nil {
			return err
		}
	}

	// 什么都不用改、编码也对得上时直接复制声音：快，而且一点不损失音质。
	// 按大小压缩时，原文件本来就不超过目标也直接复制。
	p.copy = p.start <= 0 && p.end < 0 && !p.normalize && opt.Int(conv.OptRate, 0) <= 0 && opt.Int(conv.OptChannels, 0) <= 0 &&
		userKbps <= 0 && (p.target == 0 || fileSize(p.in) <= p.target*97/100) &&
		slices.Contains(f.copyFrom, info.AudioCodec) && (f.maxCh == 0 || info.Channels <= f.maxCh)
	p.cover = f.cover && info.CoverIndex >= 0 && (info.CoverCodec == "mjpeg" || info.CoverCodec == "png")

	tmp, err := j.TempDir()
	if err != nil {
		return err
	}
	p.out = j.OutFile(f.ext)

	for attempt := 0; ; attempt++ {
		copied := p.copy
		if err := p.encode(ctx, tmp); err != nil {
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
			p.copy, p.cover = false, false // 复制出来还是太大（比如带着大封面），改成重新编码
			continue
		}
		if f.lossless {
			return conv.Fail(fmt.Sprintf("%s 是无损格式，压不到 %s（结果是 %s）", strings.ToUpper(f.name), humanSize(p.target), humanSize(sz)),
				"想要小文件建议转成 MP3")
		}
		k := p.clampKbps(p.kbps*float64(p.target)/float64(sz)*0.97, true)
		if attempt >= 2 || k >= p.kbps {
			return conv.Fail(fmt.Sprintf("压不到 %s，结果是 %s", humanSize(p.target), humanSize(sz)), "可以把文件大小放宽一些，或者先截取一段")
		}
		p.kbps = k
	}
}

// clampKbps 把码率限制在格式允许的范围里；只能用固定几档时，down 为 true 取不超过的那一档，否则取最接近的
func (p *aplan) clampKbps(k float64, down bool) float64 {
	f := p.f
	k = min(max(k, float64(f.minKbps)), float64(f.maxKbps))
	if f.rates == nil {
		return math.Floor(k)
	}
	if down {
		return snapDown(k, f.rates)
	}
	best := f.rates[0]
	for _, r := range f.rates {
		if math.Abs(r-k) < math.Abs(best-k) {
			best = r
		}
	}
	return best
}

// planTarget 按目标大小算码率
func (p *aplan) planTarget() error {
	f := p.f
	if p.length <= 0 {
		return conv.Fail("读不出音频时长，没法按文件大小压缩", "")
	}
	if f.lossless {
		if f.name == "flac" {
			return nil // FLAC 压出来多大说不准，转完再看
		}
		ch, rate := p.ch, p.rate
		if ch == 0 {
			ch = max(p.info.Channels, 1)
		}
		if rate == 0 {
			rate = max(p.info.SampleRate, 8000)
		}
		est := int64(p.length * float64(rate*ch*2))
		if est > p.target {
			return conv.Fail(fmt.Sprintf("%s 是无损格式，这段音频大约有 %s，压不到 %s", strings.ToUpper(f.name), humanSize(est), humanSize(p.target)),
				"想要小文件建议转成 MP3")
		}
		return nil
	}
	k := float64(p.target) * 8 * 0.98 / p.length / 1000 // 留 2% 给文件头
	if k < float64(f.minKbps) {
		need := int64(float64(f.minKbps) * 1000 * p.length / 8 / 0.98)
		return conv.Fail(fmt.Sprintf("音频太长，压不到 %s（%s 格式至少要 %s 左右）", humanSize(p.target), strings.ToUpper(f.name), humanSize(need)),
			"可以先截取一段，或者把文件大小放宽一些")
	}
	p.kbps = min(p.kbps, p.clampKbps(k, true))
	return nil
}

func nearestInt(v int, list []int) int {
	best := list[0]
	for _, x := range list {
		if abs(x-v) < abs(best-v) {
			best = x
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// args 生成 ffmpeg 参数
func (p *aplan) args(copyAudio, cover bool) []string {
	var a []string
	if p.start > 0 {
		a = append(a, "-ss", secs(p.start))
	}
	a = append(a, "-i", ffPath(p.in))
	if p.end > 0 {
		a = append(a, "-t", secs(p.end-max(p.start, 0)))
	}
	a = append(a, "-map", fmt.Sprintf("0:%d", p.info.AudioIndex))
	if cover {
		a = append(a, "-map", fmt.Sprintf("0:%d", p.info.CoverIndex), "-c:v", "copy", "-disposition:v:0", "attached_pic")
	} else {
		a = append(a, "-vn")
	}
	a = append(a, "-sn", "-dn", "-map_metadata", "0")
	if p.start > 0 || p.end > 0 {
		a = append(a, "-map_chapters", "-1")
	}
	if copyAudio {
		a = append(a, "-c:a", "copy")
	} else {
		a = append(a, "-c:a", p.f.codec)
		if p.kbps > 0 {
			a = append(a, "-b:a", strconv.FormatFloat(p.kbps, 'f', -1, 64)+"k")
		}
		if p.normalize {
			a = append(a, "-af", "loudnorm=I=-16:TP=-1.5:LRA=11")
		}
		if p.ch > 0 {
			a = append(a, "-ac", strconv.Itoa(p.ch))
		}
		if p.rate > 0 {
			a = append(a, "-ar", strconv.Itoa(p.rate))
		}
	}
	a = append(a, p.f.extra...)
	return append(a, "-f", p.f.muxer, ffPath(p.out))
}

// encode 转换一次。复制或者带封面出问题时，换成最保守的方式再试。
func (p *aplan) encode(ctx context.Context, tmp string) error {
	p.j.Report(0, p.note)
	prog := func(f float64) { p.j.Report(f, p.note) }
	err := runFFmpeg(ctx, p.args(p.copy, p.cover), tmp, p.length, prog)
	multi := p.ch == 0 && p.info.Channels > 2 // 有些多声道排列（比如 7.1 转 OGG）编码器不认，改成立体声
	if err != nil && !noRetry(err) && (p.copy || p.cover || multi) {
		p.copy, p.cover = false, false
		if multi {
			p.ch = 2
		}
		err = runFFmpeg(ctx, p.args(false, false), tmp, p.length, prog)
	}
	return friendly(err, "音频转换失败")
}
