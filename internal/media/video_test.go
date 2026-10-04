package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
)

func TestProbe(t *testing.T) {
	info := probe(t, fxVideo(t))
	if !near(info.Duration, 10, 0.1) || info.Width != 1280 || info.Height != 720 || !near(info.FPS, 30, 0.01) ||
		info.VideoCodec != "h264" || info.AudioCodec != "aac" || !info.HasVideo || !info.HasAudio || info.Bitrate <= 0 {
		t.Errorf("信息不对：%+v", info)
	}
	p := probe(t, fxPortrait(t))
	if p.Width != 360 || p.Height != 640 || p.Rotation%180 != 90 {
		t.Errorf("竖屏视频应该是 360×640：%+v", p)
	}
	a := probe(t, fxCoverMP3(t))
	if a.HasVideo || !a.HasAudio || a.CoverIndex < 0 || a.CoverCodec != "png" {
		t.Errorf("MP3 的封面不该算画面：%+v", a)
	}
	if _, err := Probe(context.Background(), filepath.Join(t.TempDir(), "不存在.mp4")); err == nil {
		t.Error("不存在的文件应该报错")
	}
}

func TestVideoAllTargets(t *testing.T) {
	in := fxShort(t) // 640×360 4 秒
	cases := []struct {
		format, vcodec   string
		wantV, wantA     string
		ext              string
		checkDur, hasAud bool
	}{
		{"mp4", "", "h264", "aac", ".mp4", true, true},
		{"mp4", "h265", "hevc", "aac", ".mp4", true, true},
		{"mp4", "av1", "av1", "aac", ".mp4", true, true},
		{"mkv", "", "h264", "aac", ".mkv", true, true},
		{"mov", "", "h264", "aac", ".mov", true, true},
		{"m4v", "", "h264", "aac", ".m4v", true, true},
		{"avi", "", "mpeg4", "mp3", ".avi", true, true},
		{"webm", "", "vp9", "opus", ".webm", true, true},
		{"webm", "av1", "av1", "opus", ".webm", true, true},
		{"flv", "", "h264", "aac", ".flv", true, true},
		{"wmv", "", "wmv2", "wmav2", ".wmv", true, true},
		{"ts", "", "h264", "aac", ".ts", true, true},
		{"mpg", "", "mpeg2video", "mp2", ".mpg", true, true},
		{"3gp", "", "h264", "aac", ".3gp", true, true},
		{"gif", "", "gif", "", ".gif", true, false},
		{"webp", "", "", "", ".webp", false, false},
	}
	for _, c := range cases {
		name := c.format
		if c.vcodec != "" {
			name += "-" + c.vcodec
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opt := conv.Options{conv.OptQuality: "low"} // 选了低画质就会重新编码（不直接复制）
			if c.vcodec != "" {
				opt[conv.OptVCodec] = c.vcodec
			}
			r := runJob(t, Video(c.format), in, opt)
			out := r.one(t)
			t.Logf("%s：%s，用时 %v", name, humanSize(fileSize(out)), r.took.Round(time.Millisecond))
			if filepath.Ext(out) != c.ext {
				t.Errorf("扩展名 %s，应该是 %s", filepath.Ext(out), c.ext)
			}
			if c.format == "webp" {
				data, _ := os.ReadFile(out)
				if len(data) < 32 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || !bytes.Contains(data[:64], []byte("ANIM")) {
					t.Error("不是动态 WebP")
				}
				return
			}
			info := probe(t, out)
			if info.VideoCodec != c.wantV || c.hasAud && info.AudioCodec != c.wantA || !c.hasAud && info.HasAudio {
				t.Errorf("编码是 %s/%s，应该是 %s/%s", info.VideoCodec, info.AudioCodec, c.wantV, c.wantA)
			}
			if c.checkDur && !near(info.Duration, 4, 0.25) {
				t.Errorf("时长 %.2f 秒，应该是 4 秒", info.Duration)
			}
			if c.format == "gif" {
				if info.Width != 480 || info.Height != 270 {
					t.Errorf("GIF 默认宽 480，实际 %d×%d", info.Width, info.Height)
				}
			} else if info.Width != 640 || info.Height != 360 {
				t.Errorf("尺寸变了：%d×%d", info.Width, info.Height)
			}
		})
	}
}

func TestVideoRemux(t *testing.T) {
	// MKV（H.264 + AAC）转 MP4：不用重新编码，应该很快
	r := runJob(t, Video("mp4"), fxShortMKV(t), nil)
	out := r.one(t)
	info := probe(t, out)
	if info.VideoCodec != "h264" || info.AudioCodec != "aac" || !near(info.Duration, 4, 0.1) {
		t.Errorf("结果不对：%+v", info)
	}
	if !strings.Contains(strings.Join(r.notes, "|"), "原画质") {
		t.Errorf("应该走直接复制：%v", r.notes)
	}
	t.Logf("MKV→MP4 直接复制用时 %v", r.took.Round(time.Millisecond))

	// 原文件本来就比目标小：直接复制，不重新压
	r = runJob(t, Video("mp4"), fxShortMKV(t), conv.Options{conv.OptTargetSize: itoa(20 << 20)})
	if out := r.one(t); !strings.Contains(strings.Join(r.notes, "|"), "原画质") || fileSize(out) > 20<<20 {
		t.Errorf("应该直接复制：%v", r.notes)
	}
	// 选了低画质就重新编码
	r = runJob(t, Video("mp4"), fxShortMKV(t), conv.Options{conv.OptQuality: "low"})
	if r.one(t); strings.Contains(strings.Join(r.notes, "|"), "原画质") {
		t.Errorf("低画质应该重新编码：%v", r.notes)
	}

	// 竖屏视频直接复制：旋转信息保留，播放时还是竖的
	out = runJob(t, Video("mp4"), fxPortrait(t), nil).one(t)
	if p := probe(t, out); p.Width != 360 || p.Height != 640 {
		t.Errorf("竖屏视频复制后应该还是 360×640：%+v", p)
	}
	// 竖屏视频重新编码（缩小）：画面本身转正，宽高对
	out = runJob(t, Video("mp4"), fxPortrait(t), conv.Options{"resize": "long", "long": "320"}).one(t)
	if p := probe(t, out); p.Width != 180 || p.Height != 320 || p.Rotation != 0 {
		t.Errorf("竖屏视频重新编码后应该是 180×320 不带旋转：%+v", p)
	}
}

func TestVideoResize(t *testing.T) {
	in := fxVideo(t) // 1280×720
	cases := []struct {
		name string
		opt  conv.Options
		w, h int
	}{
		{"cover 720×720", conv.Options{"w": "720", "h": "720", "fit": "cover"}, 720, 720},
		{"pad 1080×1920", conv.Options{"w": "1080", "h": "1920", "fit": "pad"}, 1080, 1920},
		{"奇数宽高取偶数", conv.Options{"w": "853", "h": "481", "fit": "stretch"}, 854, 482},
		{"480p", conv.Options{"w": "854", "h": "480"}, 854, 480},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.opt[conv.OptEnd] = "2" // 只转 2 秒，快一点
			out := runJob(t, Video("mp4"), in, c.opt).one(t)
			info := probe(t, out)
			if info.Width != c.w || info.Height != c.h {
				t.Errorf("尺寸 %d×%d，应该是 %d×%d", info.Width, info.Height, c.w, c.h)
			}
			if !near(info.Duration, 2, 0.15) {
				t.Errorf("时长 %.2f，应该是 2 秒", info.Duration)
			}
		})
	}
}

func TestVideoTrimMuteFPS(t *testing.T) {
	in := fxVideo(t)
	t.Run("截取 2-5 秒", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Video("mp4"), in, conv.Options{"start": "2", "end": "0:05"}).one(t))
		if !near(info.Duration, 3, 0.1) || !info.HasAudio {
			t.Errorf("时长 %.2f，应该是 3 秒：%+v", info.Duration, info)
		}
	})
	t.Run("去掉声音", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Video("mkv"), in, conv.Options{"mute": "1", "end": "2"}).one(t))
		if info.HasAudio {
			t.Error("应该没有声音")
		}
	})
	t.Run("改帧率", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Video("mp4"), in, conv.Options{"fps": "15", "end": "2"}).one(t))
		if !near(info.FPS, 15, 0.1) {
			t.Errorf("帧率 %.2f，应该是 15", info.FPS)
		}
	})
	t.Run("截取不合理", func(t *testing.T) {
		t.Parallel()
		ue := runJob(t, Video("mp4"), in, conv.Options{"start": "20"}).userErr(t)
		if !strings.Contains(ue.Msg, "开始时间") {
			t.Errorf("错误信息不对：%v", ue)
		}
		ue = runJob(t, Video("mp4"), in, conv.Options{"start": "5", "end": "3"}).userErr(t)
		if !strings.Contains(ue.Msg, "结束时间") {
			t.Errorf("错误信息不对：%v", ue)
		}
	})
	t.Run("GIF 截取+帧率", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Video("gif"), in, conv.Options{"start": "1", "end": "3", "fps": "10", "w": "320"}).one(t))
		if info.Width != 320 || info.Height != 180 || !near(info.Duration, 2, 0.2) {
			t.Errorf("GIF 结果不对：%+v", info)
		}
	})
}

func TestVideoTargetSize(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过比较慢的压缩测试")
	}
	in := fxVideo(t) // 10 秒 720p
	// 临时文件夹也放到带中文的路径里（中文用户名的电脑就是这样），两遍编码的日志文件要能写进去
	tmp := filepath.Join(t.TempDir(), "临时 文件夹")
	os.MkdirAll(tmp, 0o755)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	cases := []struct {
		name, format string
		opt          conv.Options
		size         int64
	}{
		{"mp4 1MB", "mp4", nil, 1 << 20},
		{"mp4 h265 600KB", "mp4", conv.Options{conv.OptVCodec: "h265"}, 600 << 10},
		{"webm 800KB", "webm", nil, 800 << 10},
		{"avi 1.5MB", "avi", nil, 1536 << 10},
		{"gif 1MB", "gif", conv.Options{"end": "4"}, 1 << 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opt := c.opt.Clone()
			opt[conv.OptTargetSize] = itoa(c.size)
			r := runJob(t, Video(c.format), in, opt)
			out := r.one(t)
			sz := fileSize(out)
			info := probe(t, out)
			t.Logf("%s：%s，%d×%d，用时 %v，说明 %v", c.name, humanSize(sz), info.Width, info.Height, r.took.Round(time.Millisecond), r.notes)
			if sz > c.size {
				t.Fatalf("结果 %s 超过了目标 %s", humanSize(sz), humanSize(c.size))
			}
			if c.format != "gif" && !near(info.Duration, 10, 0.2) {
				t.Errorf("时长 %.2f，应该是 10 秒", info.Duration)
			}
		})
	}
	t.Run("太小压不到", func(t *testing.T) {
		ue := runJob(t, Video("mp4"), in, conv.Options{conv.OptTargetSize: "50000"}).userErr(t)
		if !strings.Contains(ue.Msg, "压不到") {
			t.Errorf("错误信息不对：%v", ue)
		}
		t.Log(ue)
	})
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("临时文件没删干净：%v", entries)
	}
}

func TestVideoHDR(t *testing.T) {
	out := runJob(t, Video("mp4"), fxHDR(t), nil).one(t)
	info := probe(t, out)
	if info.VideoCodec != "h264" || info.PixFmt != "yuv420p" || info.ColorTRC == "arib-std-b67" {
		t.Errorf("HDR 应该转成普通 8 位画面：%+v", info)
	}
}

func TestVideoGPU(t *testing.T) {
	needTools(t)
	start := time.Now()
	encs := GPUEncoders(context.Background())
	t.Logf("显卡编码器：%v（检测用时 %v）", encs, time.Since(start).Round(time.Millisecond))
	start = time.Now()
	if again := GPUEncoders(context.Background()); len(again) != len(encs) || time.Since(start) > 50*time.Millisecond {
		t.Error("第二次应该直接用缓存")
	}
	in := fxVideo(t)
	for _, codec := range []string{"h264", "h265", "av1"} {
		t.Run(codec, func(t *testing.T) {
			r := runJob(t, Video("mp4"), in, conv.Options{conv.OptGPU: "1", conv.OptVCodec: codec, conv.OptEnd: "6"}) // 截取一段就不会直接复制
			out := r.one(t)
			info := probe(t, out)
			want := ffCodecName[codec]
			if info.VideoCodec != want {
				t.Errorf("编码 %s，应该是 %s", info.VideoCodec, want)
			}
			used := strings.Contains(strings.Join(r.notes, "|"), "显卡")
			t.Logf("%s：显卡=%v，%s，用时 %v", codec, used, humanSize(fileSize(out)), r.took.Round(time.Millisecond))
			if encs[codec] != "" && !used {
				t.Errorf("有显卡编码器 %s 却没用上", encs[codec])
			}
		})
	}
	if encs["h264"] != "" {
		t.Run("显卡+目标大小", func(t *testing.T) {
			r := runJob(t, Video("mp4"), in, conv.Options{conv.OptGPU: "1", conv.OptTargetSize: itoa(1 << 20)})
			out := r.one(t)
			if sz := fileSize(out); sz > 1<<20 {
				t.Errorf("结果 %s 超过 1 MB", humanSize(sz))
			}
			t.Logf("显卡压到 1MB：%s，用时 %v", humanSize(fileSize(out)), r.took.Round(time.Millisecond))
		})
	}
}

func TestVideoCancel(t *testing.T) {
	in := fxVideo(t)
	out := filepath.Join(t.TempDir(), "输出 文件夹")
	os.MkdirAll(out, 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(1500*time.Millisecond, cancel)
	start := time.Now()
	r := runJobIn(t, Video("mkv"), in, conv.Options{conv.OptVCodec: "h265", conv.OptQuality: "high"}, out, ctx)
	took := time.Since(start)
	if !errors.Is(r.err, conv.ErrCancelled) {
		t.Fatalf("应该返回 ErrCancelled，实际 %v", r.err)
	}
	if took > 4*time.Second {
		t.Errorf("取消后过了 %v 才返回", took)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("取消后留下了文件：%v", entries)
	}
	t.Logf("取消用时 %v", took.Round(time.Millisecond))
}

func TestVideoErrors(t *testing.T) {
	ue := runJob(t, Video("mp4"), fxWAV(t), nil).userErr(t)
	if !strings.Contains(ue.Msg, "没有画面") {
		t.Errorf("错误信息不对：%v", ue)
	}
	bad := filepath.Join(t.TempDir(), "坏掉的 视频.mp4")
	os.WriteFile(bad, bytes.Repeat([]byte("坏"), 1000), 0o644)
	ue = runJob(t, Video("mp4"), bad, nil).userErr(t)
	if !strings.Contains(ue.Msg, "读不出") {
		t.Errorf("错误信息不对：%v", ue)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestVideoInputs(t *testing.T) {
	t.Run("GIF 动图转 MP4", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Video("mp4"), fxGIF(t), nil).one(t))
		if info.VideoCodec != "h264" || info.Width != 160 || info.Height != 120 || info.HasAudio {
			t.Errorf("结果不对：%+v", info)
		}
	})
	t.Run("奇数宽高、5.1 声道", func(t *testing.T) {
		t.Parallel()
		in := fixture(t, "奇数 5.1.mkv", ffmpegCmd(
			"-f", "lavfi", "-i", "testsrc2=size=321x241:rate=25:duration=2", "-f", "lavfi", "-i", "sine=duration=2",
			"-filter_complex", "[1]pan=5.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0[a]", "-map", "0:v", "-map", "[a]",
			"-c:v", "ffv1", "-c:a", "aac", "-f", "matroska"))
		for _, f := range []string{"mp4", "avi", "webm", "wmv", "mpg"} {
			info := probe(t, runJob(t, Video(f), in, nil).one(t))
			if info.Width != 320 || info.Height != 240 || !info.HasAudio {
				t.Errorf("%s：结果不对 %+v", f, info)
			}
		}
	})
	t.Run("保持原格式", func(t *testing.T) {
		t.Parallel()
		out := runJob(t, Video("same"), fxShortMKV(t), conv.Options{"end": "2"}).one(t)
		if filepath.Ext(out) != ".mkv" {
			t.Errorf("应该还是 mkv：%s", out)
		}
	})
}

func TestVideoGIFEdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过比较慢的压缩测试")
	}
	// 声音在前、画面在后的文件；GIF 走「先生成调色板再上色」的两步做法（很长的视频就是这样）
	in := fixture(t, "声音在前.mkv", ffmpegCmd(
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=3", "-f", "lavfi", "-i", "sine=duration=3",
		"-map", "1:a", "-map", "0:v", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-f", "matroska"))
	if p := probe(t, in); p.VideoIndex != 1 || p.AudioIndex != 0 {
		t.Fatalf("素材不对：%+v", p)
	}
	old := gifBufferLimit
	gifBufferLimit = 1
	r := runJob(t, Video("gif"), in, nil)
	gifBufferLimit = old
	info := probe(t, r.one(t))
	if info.VideoCodec != "gif" || info.Width != 480 || !near(info.Duration, 3, 0.2) {
		t.Errorf("结果不对：%+v", info)
	}
	if !strings.Contains(strings.Join(r.notes, "|"), "调色板") {
		t.Errorf("应该分两步：%v", r.notes)
	}

	// 动图压到指定大小：太大就缩小画面
	r = runJob(t, Video("gif"), fxVideo(t), conv.Options{"end": "4", conv.OptTargetSize: itoa(300 << 10)})
	out := r.one(t)
	info = probe(t, out)
	t.Logf("GIF 压到 300KB：%s，%d×%d，%v", humanSize(fileSize(out)), info.Width, info.Height, r.notes)
	if fileSize(out) > 300<<10 || info.Width >= 480 {
		t.Errorf("结果 %s %d×%d 不对", humanSize(fileSize(out)), info.Width, info.Height)
	}
	r = runJob(t, Video("webp"), fxVideo(t), conv.Options{"end": "4", conv.OptTargetSize: itoa(200 << 10)})
	if out := r.one(t); fileSize(out) > 200<<10 {
		t.Errorf("WebP 结果 %s 超过 200 KB", humanSize(fileSize(out)))
	}

	// 补边颜色
	out = runJob(t, Video("mp4"), fxShort(t), conv.Options{"w": "640", "h": "640", "fit": "pad", "bg": "#ff0000", "end": "1"}).one(t)
	png := filepath.Join(t.TempDir(), "帧.png")
	if err := ffmpegCmd("-i", out, "-frames:v", "1", "-f", "image2")(png); err != nil {
		t.Fatal(err)
	}
	if px := pixel(t, png, 320, 10); !looksRed(px) {
		t.Errorf("补边应该是红色，实际 %s", px)
	}
}

func TestVideoGPUFallback(t *testing.T) {
	// 显卡编码失败（这里用一个不存在的编码器模拟）时，自动改用 CPU
	in := fxShort(t)
	gpuMu.Lock()
	saved := gpuCache
	gpuCache = map[string]string{"h264": "h264_不存在"}
	gpuMu.Unlock()
	defer func() {
		gpuMu.Lock()
		gpuCache = saved
		gpuMu.Unlock()
	}()
	r := runJob(t, Video("mp4"), in, conv.Options{conv.OptGPU: "1", conv.OptQuality: "low"})
	info := probe(t, r.one(t))
	if info.VideoCodec != "h264" || !strings.Contains(strings.Join(r.notes, "|"), "改用 CPU") {
		t.Errorf("应该改用 CPU 编码成功：%+v %v", info, r.notes)
	}
}
