package media

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
)

func TestAudioAllTargets(t *testing.T) {
	in := fxWAV(t) // 45 秒立体声
	cases := []struct {
		format, codec, ext string
		dur                float64
	}{
		{"mp3", "mp3", ".mp3", 45},
		{"m4a", "aac", ".m4a", 45},
		{"aac", "aac", ".aac", 45},
		{"wav", "pcm_s16le", ".wav", 45},
		{"flac", "flac", ".flac", 45},
		{"ogg", "vorbis", ".ogg", 45},
		{"opus", "opus", ".opus", 45},
		{"wma", "wmav2", ".wma", 45},
		{"aiff", "pcm_s16be", ".aiff", 45},
		{"ac3", "ac3", ".ac3", 45},
		{"amr", "amr_nb", ".amr", 45},
		{"m4r", "aac", ".m4r", 40}, // iPhone 铃声最长 40 秒
		{"same", "pcm_s16le", ".wav", 45},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			t.Parallel()
			r := runJob(t, Audio(c.format), in, nil)
			out := r.one(t)
			if filepath.Ext(out) != c.ext {
				t.Errorf("扩展名 %s，应该是 %s", filepath.Ext(out), c.ext)
			}
			info := probe(t, out)
			t.Logf("%s：%s，%d Hz %d 声道，用时 %v", c.format, humanSize(fileSize(out)), info.SampleRate, info.Channels, r.took.Round(time.Millisecond))
			if info.AudioCodec != c.codec || info.HasVideo {
				t.Errorf("编码 %s，应该是 %s", info.AudioCodec, c.codec)
			}
			tol := 0.15
			if c.format == "aac" || c.format == "ac3" || c.format == "amr" {
				tol = 2 // 裸流没有时长信息，ffprobe 是按码率估的
			}
			if !near(info.Duration, c.dur, tol) {
				t.Errorf("时长 %.2f，应该是 %.0f 秒", info.Duration, c.dur)
			}
			switch c.format {
			case "amr":
				if info.SampleRate != 8000 || info.Channels != 1 {
					t.Errorf("AMR 应该是 8000 Hz 单声道：%d Hz %d 声道", info.SampleRate, info.Channels)
				}
			case "m4r":
				if !strings.Contains(strings.Join(r.notes, "|"), "40 秒") {
					t.Errorf("应该提示只保留 40 秒：%v", r.notes)
				}
			}
		})
	}
}

func TestAudioFromVideo(t *testing.T) {
	in := fxVideo(t) // 10 秒，AAC 声音
	t.Run("提取成 m4a 直接复制", func(t *testing.T) {
		r := runJob(t, Audio("m4a"), in, nil)
		info := probe(t, r.one(t))
		if info.HasVideo || info.AudioCodec != "aac" || !near(info.Duration, 10, 0.1) {
			t.Errorf("结果不对：%+v", info)
		}
		t.Logf("视频→m4a 用时 %v", r.took.Round(time.Millisecond))
	})
	t.Run("转 mp3", func(t *testing.T) {
		info := probe(t, runJob(t, Audio("mp3"), in, conv.Options{conv.OptABitrate: "128"}).one(t))
		if info.HasVideo || info.AudioCodec != "mp3" || !near(info.Duration, 10, 0.15) || !near(float64(info.AudioBitrate), 128000, 2000) {
			t.Errorf("结果不对：%+v", info)
		}
	})
	t.Run("没有声音", func(t *testing.T) {
		ue := runJob(t, Audio("mp3"), fxSilent(t), nil).userErr(t)
		if ue.Msg != "这个文件里没有声音" {
			t.Errorf("错误信息不对：%v", ue)
		}
	})
}

func TestAudioOptions(t *testing.T) {
	in := fxWAV(t)
	t.Run("采样率 声道 标准化", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Audio("mp3"), in, conv.Options{"rate": "22050", "ch": "1", "normalize": "1"}).one(t))
		if info.SampleRate != 22050 || info.Channels != 1 {
			t.Errorf("应该是 22050 Hz 单声道：%+v", info)
		}
	})
	t.Run("标准化保持采样率", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Audio("flac"), in, conv.Options{"normalize": "1"}).one(t))
		if info.SampleRate != 44100 {
			t.Errorf("采样率应该还是 44100：%d", info.SampleRate)
		}
	})
	t.Run("opus 不支持的采样率", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Audio("opus"), in, conv.Options{"rate": "44100"}).one(t))
		if info.AudioCodec != "opus" {
			t.Errorf("结果不对：%+v", info)
		}
	})
	t.Run("截取", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Audio("m4r"), in, conv.Options{"start": "10", "end": "25"}).one(t))
		if !near(info.Duration, 15, 0.1) {
			t.Errorf("时长 %.2f，应该是 15", info.Duration)
		}
	})
	t.Run("铃声从中间开始", func(t *testing.T) {
		t.Parallel()
		info := probe(t, runJob(t, Audio("m4r"), in, conv.Options{"start": "3"}).one(t))
		if !near(info.Duration, 40, 0.1) {
			t.Errorf("时长 %.2f，应该是 40", info.Duration)
		}
	})
}

func TestAudioCover(t *testing.T) {
	in := fxCoverMP3(t)
	for _, f := range []string{"m4a", "flac", "mp3"} {
		t.Run(f, func(t *testing.T) {
			info := probe(t, runJob(t, Audio(f), in, conv.Options{conv.OptABitrate: "160"}).one(t))
			if info.CoverIndex < 0 || info.HasVideo {
				t.Errorf("专辑封面应该保留：%+v", info)
			}
		})
	}
	// 不能带封面的格式不带
	if info := probe(t, runJob(t, Audio("ogg"), in, nil).one(t)); info.CoverIndex >= 0 || info.HasVideo {
		t.Errorf("ogg 不该有画面：%+v", info)
	}
}

func TestAudioTargetSize(t *testing.T) {
	in := fxWAV(t) // 45 秒
	for _, c := range []struct {
		format string
		size   int64
	}{{"mp3", 300 << 10}, {"m4a", 200 << 10}, {"opus", 100 << 10}, {"ogg", 400 << 10}, {"wma", 250 << 10}, {"amr", 40 << 10}} {
		t.Run(c.format, func(t *testing.T) {
			t.Parallel()
			out := runJob(t, Audio(c.format), in, conv.Options{conv.OptTargetSize: itoa(c.size)}).one(t)
			sz := fileSize(out)
			t.Logf("%s 目标 %s：%s", c.format, humanSize(c.size), humanSize(sz))
			if sz > c.size {
				t.Errorf("结果 %s 超过了 %s", humanSize(sz), humanSize(c.size))
			}
			if sz < c.size*6/10 {
				t.Errorf("结果 %s 比目标 %s 小太多，浪费了音质", humanSize(sz), humanSize(c.size))
			}
		})
	}
	t.Run("无损格式", func(t *testing.T) {
		ue := runJob(t, Audio("wav"), in, conv.Options{conv.OptTargetSize: itoa(1 << 20)}).userErr(t)
		if !strings.Contains(ue.Detail+ue.Msg, "MP3") {
			t.Errorf("应该建议转成 MP3：%v", ue)
		}
		ue = runJob(t, Audio("flac"), in, conv.Options{conv.OptTargetSize: itoa(100 << 10)}).userErr(t)
		if !strings.Contains(ue.Detail+ue.Msg, "MP3") {
			t.Errorf("应该建议转成 MP3：%v", ue)
		}
		// 本来就够小时不报错
		runJob(t, Audio("wav"), in, conv.Options{conv.OptTargetSize: itoa(10 << 20)}).one(t)
	})
	t.Run("太小", func(t *testing.T) {
		ue := runJob(t, Audio("mp3"), in, conv.Options{conv.OptTargetSize: itoa(20 << 10)}).userErr(t)
		if !strings.Contains(ue.Msg, "压不到") {
			t.Errorf("错误信息不对：%v", ue)
		}
		t.Log(ue)
	})
}

func TestAudioMultichannel(t *testing.T) {
	// 7.1 声道转 OGG：libvorbis 不认这种排列，自动改成立体声
	in := fixture(t, "环绕 7.1.wav", ffmpegCmd("-f", "lavfi", "-i", "sine=duration=3",
		"-af", "pan=7.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0|c6=c0|c7=c0", "-c:a", "pcm_s16le", "-f", "wav"))
	for _, f := range []string{"ogg", "opus", "m4a", "mp3", "flac"} {
		info := probe(t, runJob(t, Audio(f), in, nil).one(t))
		if !info.HasAudio || info.Channels < 2 {
			t.Errorf("%s：结果不对 %+v", f, info)
		}
	}
}
