package media

import (
	"context"
	"maps"
	"runtime"
	"sync"
	"time"

	"github.com/haoawake/omni-convert/internal/tools"
)

var (
	gpuMu    sync.Mutex
	gpuCache map[string]string
)

// GPUEncoders 检测这台电脑的显卡能编码哪些格式，返回「编码 → ffmpeg 编码器」，
// 比如 {"h264": "h264_nvenc", "h265": "hevc_nvenc", "av1": "av1_nvenc"}。
// 检测方法是真的用显卡编几帧测试画面（光看驱动在不在不可靠）。优先 NVIDIA，其次 AMD、Intel；
// macOS 上是苹果芯片的媒体引擎（VideoToolbox）。结果只检测一次，之后直接用缓存。
func GPUEncoders(ctx context.Context) map[string]string {
	gpuMu.Lock()
	defer gpuMu.Unlock()
	if gpuCache != nil {
		return maps.Clone(gpuCache)
	}
	res := map[string]string{}
	if tools.Need(tools.FFmpeg()) != nil {
		return res
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for codec, encs := range gpuCandidates() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, enc := range encs {
				if testGPUEncoder(ctx, enc, codec) {
					mu.Lock()
					res[codec] = enc
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return res // 检测被打断了，下次再测
	}
	gpuCache = res
	return maps.Clone(res)
}

// gpuCandidates 是每种编码可以用的显卡编码器，按优先级排列
func gpuCandidates() map[string][]string {
	if runtime.GOOS == "darwin" {
		// 苹果芯片的媒体引擎：H.264 和 H.265（AV1 只能解码不能编码）
		return map[string][]string{"h264": {"h264_videotoolbox"}, "h265": {"hevc_videotoolbox"}}
	}
	return map[string][]string{
		"h264": {"h264_nvenc", "h264_amf", "h264_qsv"},
		"h265": {"hevc_nvenc", "hevc_amf", "hevc_qsv"},
		"av1":  {"av1_nvenc", "av1_amf", "av1_qsv"},
	}
}

// testGPUEncoder 用和正式转换一样的参数编 10 帧测试画面，成功就说明能用
func testGPUEncoder(ctx context.Context, enc, codec string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-frames:v", "10"}
	args = append(args, gpuVideoArgs(enc, codec, 1, 0, false)...)
	args = append(args, "-f", "null", "-")
	_, err := tools.Run(ctx, tools.FFmpeg(), args, nil)
	return err == nil
}
