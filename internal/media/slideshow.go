package media

import (
	"context"
	"fmt"
	"strconv"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// Slideshow 把一张张画面（文件名按 pattern，比如 page_%04d.jpg，从 1 开始，共 n 张）做成 MP4：
// 每张停留 sec 秒，画面统一缩放到 w×h（不够的地方补黑边）。没装 PowerPoint 时「PPT 转视频」用它。
func Slideshow(ctx context.Context, j *conv.Job, pattern string, n, sec, w, h int) error {
	if err := tools.Need(tools.FFmpeg()); err != nil {
		return err
	}
	dur := float64(n * sec)
	out := j.OutFile(".mp4")
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p", w, h, w, h)
	args := []string{
		"-framerate", "1/" + strconv.Itoa(sec), "-start_number", "1", "-i", ffPath(pattern),
		"-vf", vf, "-r", "30", "-c:v", "libx264", "-preset", "medium", "-tune", "stillimage", "-crf", "20",
		"-movflags", "+faststart", "-t", secs(dur), "-f", "mp4", ffPath(out),
	}
	err := runFFmpeg(ctx, args, "", dur, func(f float64) { j.Report(0.5+f*0.5, "正在生成视频") })
	return friendly(err, "生成视频失败")
}
