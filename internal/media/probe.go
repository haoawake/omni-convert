package media

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// Info 是视频、音频文件的基本信息
type Info struct {
	Duration   float64 // 时长（秒），读不出来时为 0
	Width      int     // 画面显示宽度（已经按旋转信息转正，也考虑了非方形像素）
	Height     int     // 画面显示高度
	FPS        float64 // 帧率，读不出来时为 0
	VideoCodec string  // 比如 h264、hevc、vp9、av1；没有画面时为空
	AudioCodec string  // 比如 aac、mp3、opus；没有声音时为空
	HasVideo   bool    // 有画面（MP3、FLAC 里的专辑封面不算）
	HasAudio   bool    // 有声音
	Bitrate    int64   // 总码率（bit/s），读不出来时为 0

	Rotation     int     // 播放时要旋转的角度：0、90、180、270
	PixFmt       string  // 像素格式，比如 yuv420p
	ColorTRC     string  // 传输特性：smpte2084、arib-std-b67 表示 HDR
	SampleRate   int     // 采样率
	Channels     int     // 声道数
	AudioBitrate int64   // 音频码率（bit/s），读不出来时为 0
	VideoIndex   int     // 选用的画面流序号，没有时为 -1
	AudioIndex   int     // 选用的声音流序号，没有时为 -1
	CoverIndex   int     // 专辑封面流序号，没有时为 -1
	CoverCodec   string  // 专辑封面的编码（mjpeg、png）
	Format       string  // 容器格式（ffprobe 的 format_name）
	codedW       int     // 编码尺寸（已转正，不考虑非方形像素）
	codedH       int     //
	sar          float64 // 像素宽高比，1 = 方形像素
}

type probeJSON struct {
	Streams []struct {
		Index        int               `json:"index"`
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		SAR          string            `json:"sample_aspect_ratio"`
		PixFmt       string            `json:"pix_fmt"`
		ColorTRC     string            `json:"color_transfer"`
		RFrameRate   string            `json:"r_frame_rate"`
		AvgFrameRate string            `json:"avg_frame_rate"`
		SampleRate   string            `json:"sample_rate"`
		Channels     int               `json:"channels"`
		BitRate      string            `json:"bit_rate"`
		Duration     string            `json:"duration"`
		Disposition  map[string]int    `json:"disposition"`
		Tags         map[string]string `json:"tags"`
		SideData     []struct {
			Type     string  `json:"side_data_type"`
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// Probe 用 ffprobe 读文件信息。读不出来（文件损坏、不是音视频）时返回给用户看的错误。
func Probe(ctx context.Context, path string) (*Info, error) {
	out, err := tools.Run(ctx, tools.FFprobe(), []string{
		"-v", "error", "-print_format", "json", "-show_format", "-show_streams", ffPath(path),
	}, nil)
	if err != nil {
		return nil, friendly(err, "读不出这个文件，它可能已经损坏，或者不是音频、视频文件")
	}
	var pj probeJSON
	if err := json.Unmarshal(out, &pj); err != nil {
		return nil, conv.Fail("读不出这个文件的信息", err.Error())
	}
	info := &Info{VideoIndex: -1, AudioIndex: -1, CoverIndex: -1, Format: pj.Format.FormatName, sar: 1}
	info.Duration = parseFloat(pj.Format.Duration)
	info.Bitrate = int64(parseFloat(pj.Format.BitRate))
	streamDur := 0.0
	for _, s := range pj.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] != 0 {
				if info.CoverIndex < 0 {
					info.CoverIndex, info.CoverCodec = s.Index, s.CodecName
				}
				continue
			}
			if info.HasVideo || s.Width <= 0 || s.Height <= 0 {
				continue
			}
			info.HasVideo = true
			info.VideoIndex = s.Index
			info.VideoCodec = s.CodecName
			info.PixFmt = s.PixFmt
			info.ColorTRC = s.ColorTRC
			info.FPS = parseRate(s.AvgFrameRate)
			if r := parseRate(s.RFrameRate); info.FPS <= 0 || info.FPS > 1000 {
				info.FPS = r
			}
			if info.FPS > 500 {
				info.FPS = 0 // 有些 MKV、WebM 写的是 1000 这种假帧率，当作不知道
			}
			rot := 0.0
			for _, sd := range s.SideData {
				if sd.Type == "Display Matrix" {
					rot = sd.Rotation
				}
			}
			if rot == 0 {
				rot = -parseFloat(s.Tags["rotate"]) // 老式的 rotate 标签方向相反
			}
			info.Rotation = ((int(math.Round(-rot/90))*90)%360 + 360) % 360
			w, h := s.Width, s.Height
			if sar := parseRatio(s.SAR); sar > 0 {
				info.sar = sar
			}
			if info.Rotation == 90 || info.Rotation == 270 {
				w, h = h, w
				info.sar = 1 / info.sar
			}
			info.codedW, info.codedH = w, h
			info.Width, info.Height = w, h
			if math.Abs(info.sar-1) > 0.01 {
				info.Width = max(1, int(math.Round(float64(w)*info.sar)))
			}
			streamDur = max(streamDur, parseFloat(s.Duration))
		case "audio":
			if info.HasAudio || s.Channels <= 0 && s.SampleRate == "" {
				continue
			}
			info.HasAudio = true
			info.AudioIndex = s.Index
			info.AudioCodec = s.CodecName
			info.SampleRate = int(parseFloat(s.SampleRate))
			info.Channels = s.Channels
			info.AudioBitrate = int64(parseFloat(s.BitRate))
			streamDur = max(streamDur, parseFloat(s.Duration))
		}
	}
	if info.Duration <= 0 {
		info.Duration = streamDur
	}
	if info.Bitrate <= 0 && info.Duration > 0 {
		if sz := fileSize(path); sz > 0 {
			info.Bitrate = int64(float64(sz) * 8 / info.Duration)
		}
	}
	return info, nil
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// parseRate 解析 "30000/1001" 这样的帧率
func parseRate(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return parseFloat(s)
	}
	n, d := parseFloat(a), parseFloat(b)
	if n <= 0 || d <= 0 {
		return 0
	}
	return n / d
}

// parseRatio 解析 "16:15" 这样的比例
func parseRatio(s string) float64 {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 0
	}
	n, d := parseFloat(a), parseFloat(b)
	if n <= 0 || d <= 0 {
		return 0
	}
	return n / d
}
