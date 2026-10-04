package conv

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Options 是界面上填的选项，全部以字符串保存（方便存进设置文件、在命令行里传）。
// 没填的项用各个转换器自己的默认值。用到的键见下面的常量。
type Options map[string]string

// 选项的键。同一个键在不同转换器里含义一致。
const (
	// 尺寸（图片、视频）
	OptResize   = "resize"  // none 保持原样 | box 指定宽高 | percent 按百分比 | long 限制长边
	OptWidth    = "w"       // 像素
	OptHeight   = "h"       // 像素
	OptFit      = "fit"     // contain 等比缩放（放进框里）| cover 裁剪填满（正好 w×h）| stretch 拉伸 | pad 等比缩放后补边到 w×h
	OptPercent  = "percent" // 1~1000
	OptLong     = "long"    // 长边像素
	OptPadColor = "bg"      // 补边颜色 #RRGGBB，默认白色（视频默认黑色）

	// 大小与质量
	OptTargetSize = "size"    // 目标文件大小（字节），0 或空表示不限制
	OptQuality    = "quality" // 图片 1~100；视频 high | medium | low
	OptStrip      = "strip"   // 1 = 去掉照片里的拍摄信息（EXIF、GPS）

	// 视频
	OptVCodec = "vcodec" // h264 | h265 | av1
	OptFPS    = "fps"    // 0 或空 = 保持
	OptStart  = "start"  // 从第几秒开始（可以带小数，也可以写 1:23 / 0:01:23.5）
	OptEnd    = "end"    // 到第几秒结束
	OptMute   = "mute"   // 1 = 去掉声音
	OptGPU    = "gpu"    // 1 = 用显卡编码（有 NVIDIA / AMD / Intel 显卡时）

	// 音频
	OptABitrate  = "abitrate"  // kbps
	OptRate      = "rate"      // 采样率，0 = 保持
	OptChannels  = "ch"        // 0 = 保持，1 单声道，2 立体声
	OptNormalize = "normalize" // 1 = 音量标准化

	// 文档、PDF
	OptDPI      = "dpi"      // 转图片时的清晰度，默认 150
	OptPages    = "pages"    // 页码范围，比如 "1-3,5,8-"，空 = 全部
	OptLongW    = "longw"    // 长图宽度（像素），默认 1080
	OptSplit    = "split"    // 拆分方式：each 每页一个 | every 每 N 页一个 | ranges 按范围
	OptEvery    = "every"    // 每几页一个文件
	OptRanges   = "ranges"   // 拆分范围，比如 "1-3,4-10"：每段一个文件
	OptLevel    = "level"    // PDF 压缩强度：light 无损 | medium 推荐 | strong 强力
	OptPassword = "password" // PDF 加密 / 解密的密码
	OptAngle    = "angle"    // 旋转角度：90 | 180 | 270
	OptSlideSec = "slidesec" // PPT 转视频时每页停留几秒
	OptPageSize = "pagesize" // 图片转 PDF 的纸张：fit 跟图片一样大 | a4 A4 纵向
)

func (o Options) Str(k, def string) string {
	if v, ok := o[k]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (o Options) Int(k string, def int) int {
	v, err := strconv.Atoi(o.Str(k, ""))
	if err != nil {
		return def
	}
	return v
}

func (o Options) Int64(k string, def int64) int64 {
	v, err := strconv.ParseInt(o.Str(k, ""), 10, 64)
	if err != nil {
		return def
	}
	return v
}

func (o Options) Float(k string, def float64) float64 {
	v, err := strconv.ParseFloat(o.Str(k, ""), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

func (o Options) Bool(k string) bool {
	switch strings.ToLower(o.Str(k, "")) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Seconds 读一个时间点（秒），支持 "83.5"、"1:23.5"、"0:01:23"。没填或填错返回 -1。
func (o Options) Seconds(k string) float64 {
	v, ok := ParseClock(o.Str(k, ""))
	if !ok {
		return -1
	}
	return v
}

func (o Options) Clone() Options {
	c := make(Options, len(o))
	for k, v := range o {
		c[k] = v
	}
	return c
}

// String 按键排好序输出，日志里用
func (o Options) String() string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%s", k, o[k])
	}
	return b.String()
}

// ParseClock 解析 "83.5"、"1:23.5"、"01:02:03" 这样的时间，返回秒
func ParseClock(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "：", ":"))
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

// ParseSize 解析 "500KB"、"1.5 MB"、"800k"、"2m"、"123456"（字节）。返回字节数。
func ParseSize(s string) (int64, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "B")
	mul := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mul, s = 1024, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mul, s = 1024*1024, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mul, s = 1024*1024*1024, strings.TrimSuffix(s, "G")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return int64(v * mul), true
}

// HumanSize 把字节数写成 1.23 MB 这种样子
func HumanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / 1024
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	switch {
	case f < 10:
		return fmt.Sprintf("%.2f %s", f, units[i])
	case f < 100:
		return fmt.Sprintf("%.1f %s", f, units[i])
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}

// PageRange 是一段页码，从 1 开始，含两端
type PageRange struct{ From, To int }

// ParseRanges 解析 "1-3, 5, 8-" 这样的页码范围（n 是总页数，"8-" 表示到最后一页）。
// 空字符串表示全部页。超出范围的部分会被截掉；完全无效时返回错误。
// 也认中文逗号、顿号和「~」「—」。
func ParseRanges(s string, n int) ([]PageRange, error) {
	s = strings.NewReplacer("，", ",", "、", ",", "；", ",", ";", ",", "~", "-", "～", "-", "—", "-", "–", "-", " ", "").Replace(s)
	if s == "" {
		if n <= 0 {
			return nil, Fail("这个文件没有页面", "")
		}
		return []PageRange{{1, n}}, nil
	}
	var out []PageRange
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		a, b, isRange := strings.Cut(part, "-")
		from, to := 0, 0
		var err error
		if a == "" {
			from = 1
		} else if from, err = strconv.Atoi(a); err != nil {
			return nil, Fail("页码范围写得不对："+part, "应该像 1-3,5,8- 这样")
		}
		switch {
		case !isRange:
			to = from
		case b == "":
			to = n
		default:
			if to, err = strconv.Atoi(b); err != nil {
				return nil, Fail("页码范围写得不对："+part, "应该像 1-3,5,8- 这样")
			}
		}
		if from > to {
			from, to = to, from
		}
		from = max(from, 1)
		to = min(to, n)
		if from > to {
			continue // 整段超出总页数
		}
		out = append(out, PageRange{from, to})
	}
	if len(out) == 0 {
		return nil, Fail(fmt.Sprintf("页码范围超出了总页数（共 %d 页）", n), "")
	}
	return out, nil
}

// Pages 把范围展开成页码列表（从 1 开始），保持写的顺序，去掉重复
func Pages(ranges []PageRange) []int {
	seen := map[int]bool{}
	var out []int
	for _, r := range ranges {
		for p := r.From; p <= r.To; p++ {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
