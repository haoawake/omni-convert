package media

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// 没装 PowerPoint 时「PPT 转视频」用的幻灯片视频：每页停留的秒数、画面大小要对
func TestSlideshow(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	for i, size := range []string{"640x360", "300x400", "800x450"} { // 第二页是竖的，要补黑边
		p := filepath.Join(dir, fmt.Sprintf("page_%04d.jpg", i+1))
		if _, err := tools.Run(context.Background(), tools.FFmpeg(), []string{"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=" + size, "-frames:v", "1", p}, nil); err != nil {
			t.Fatal(err)
		}
	}
	j := conv.NewJob([]string{filepath.Join(dir, "演示 文稿.pptx")}, "doc:mp4", nil, dir, nil)
	if err := Slideshow(context.Background(), j, filepath.Join(dir, "page_%04d.jpg"), 3, 2, 1280, 720); err != nil {
		t.Fatal(err)
	}
	j.Finish(false)
	outs := j.Outputs()
	if len(outs) != 1 {
		t.Fatalf("输出：%v", outs)
	}
	info, err := Probe(context.Background(), outs[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.VideoCodec != "h264" || info.Width != 1280 || info.Height != 720 || math.Abs(info.Duration-6) > 0.6 {
		t.Fatalf("视频不对：%s %dx%d %.2f 秒", info.VideoCodec, info.Width, info.Height, info.Duration)
	}
}
