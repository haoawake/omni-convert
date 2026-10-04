package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// 测试素材放在一个名字里有中文和空格的临时文件夹里，所有测试共用，跑完删掉
var (
	fxRoot string
	fxMu   sync.Mutex
	fxDone = map[string]error{}
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fxRoot != "" {
		os.RemoveAll(fxRoot)
	}
	os.Exit(code)
}

func needTools(t testing.TB) {
	t.Helper()
	if tools.Root() == "" {
		t.Skip("没有找到 tools 文件夹（ffmpeg、ImageMagick），跳过")
	}
}

// fixture 返回素材文件的路径，第一次用到时才生成（make 负责写出 path）
func fixture(t testing.TB, name string, make func(path string) error) string {
	t.Helper()
	needTools(t)
	fxMu.Lock()
	defer fxMu.Unlock()
	if fxRoot == "" {
		d, err := os.MkdirTemp("", "媒体 测试 素材 *")
		if err != nil {
			t.Fatal(err)
		}
		fxRoot = d
	}
	p := filepath.Join(fxRoot, name)
	err, ok := fxDone[name]
	if !ok {
		err = make(p)
		fxDone[name] = err
	}
	if err != nil {
		t.Fatalf("生成测试素材 %s 失败：%v", name, err)
	}
	return p
}

func magickCmd(args ...string) func(string) error {
	return func(p string) error {
		_, err := tools.Run(context.Background(), tools.Magick(), append(args, p), nil)
		return err
	}
}

func ffmpegCmd(args ...string) func(string) error {
	return func(p string) error {
		a := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}, args...)
		_, err := tools.Run(context.Background(), tools.FFmpeg(), append(a, p), nil)
		return err
	}
}

// ---------------------------------------------------------------- 图片素材

func fxPhoto(t testing.TB) string { // 1200×800 的「照片」
	return fixture(t, "照片 横.jpg", magickCmd("-seed", "3", "-size", "1200x800", "plasma:tomato-steelblue", "-blur", "0x1", "-quality", "92"))
}

func fxBigPhoto(t testing.TB) string { // 3000×2000，细节很多，用来测压缩
	return fixture(t, "大 照片.jpg", magickCmd("-seed", "7", "-size", "3000x2000", "plasma:fractal", "-blur", "0x0.6", "-quality", "95"))
}

func fxAlphaPNG(t testing.TB) string { // 400×300，透明背景
	return fixture(t, "透明 图标.png", magickCmd("-size", "400x300", "xc:none",
		"-fill", "rgba(220,30,30,1)", "-draw", "circle 200,150 200,40",
		"-fill", "rgba(30,30,220,0.5)", "-draw", "rectangle 20,20 120,120"))
}

func fxGIF(t testing.TB) string { // 4 帧动图 160×120
	return fixture(t, "动 图.gif", magickCmd("-delay", "20",
		"-size", "160x120", "gradient:red-blue", "gradient:lime-black", "gradient:yellow-purple", "gradient:white-navy", "-loop", "0"))
}

func fxCMYK(t testing.TB) string {
	return fixture(t, "CMYK 印刷.jpg", magickCmd("logo:", "-colorspace", "CMYK", "-quality", "90"))
}

func fxICO(t testing.TB) string { // 256、48、16 三种尺寸的图标
	return fixture(t, "多 尺寸.ico", magickCmd("-size", "256x256", "gradient:red-blue", "-define", "icon:auto-resize=256,48,16"))
}

func fxSVG(t testing.TB) string { // 48×48 的小矢量图
	return fixture(t, "矢量 图标.svg", func(p string) error {
		return os.WriteFile(p, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 48 48">`+
			`<circle cx="24" cy="24" r="20" fill="#e33"/><rect x="4" y="4" width="10" height="10" fill="#33e"/></svg>`), 0o644)
	})
}

// fxRotated 是 300×200 的 JPEG（左半红、右半蓝），EXIF 里记着「要顺时针转 90°」，转正后是 200×300、上半红
func fxRotated(t testing.TB) string {
	return fixture(t, "手机 竖拍.jpg", func(p string) error {
		if err := magickCmd("-size", "300x200", "xc:blue", "-fill", "red", "-draw", "rectangle 0,0 149,199", "-quality", "95")(p); err != nil {
			return err
		}
		return addEXIFOrientation(p, 6)
	})
}

// addEXIFOrientation 在 JPEG 里插一段只有「方向」一项的 EXIF
func addEXIFOrientation(p string, orient uint16) error {
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return errors.New("不是 JPEG")
	}
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a\x00\x00\x00\x08")
	binary.Write(&tiff, binary.BigEndian, uint16(1))           // 1 项
	binary.Write(&tiff, binary.BigEndian, []uint16{0x0112, 3}) // Orientation，SHORT
	binary.Write(&tiff, binary.BigEndian, uint32(1))           // 个数
	binary.Write(&tiff, binary.BigEndian, []uint16{orient, 0}) // 值
	binary.Write(&tiff, binary.BigEndian, uint32(0))           // 没有下一个 IFD
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	out := append([]byte{0xFF, 0xD8}, seg...)
	out = append(out, payload...)
	out = append(out, data[2:]...)
	return os.WriteFile(p, out, 0o644)
}

// ---------------------------------------------------------------- 音视频素材

func fxVideo(t testing.TB) string { // 1280×720 30fps 10 秒，带立体声
	return fixture(t, "测试 视频.mp4", ffmpegCmd(
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=10:sample_rate=48000",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-shortest", "-f", "mp4"))
}

func fxShort(t testing.TB) string { // 640×360 30fps 4 秒，带声音
	return fixture(t, "短 视频.mp4", ffmpegCmd(
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=550:duration=4:sample_rate=44100",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "22", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-shortest", "-f", "mp4"))
}

func fxShortMKV(t testing.TB) string {
	src := fxShort(t)
	return fixture(t, "短 视频.mkv", ffmpegCmd("-i", src, "-c", "copy", "-f", "matroska"))
}

func fxPortrait(t testing.TB) string { // 编码是 640×360，但标着「旋转 90°」，播放时是竖屏 360×640
	src := fxShort(t)
	return fixture(t, "竖 屏.mp4", ffmpegCmd("-display_rotation", "90", "-i", src, "-c", "copy", "-f", "mp4"))
}

func fxHDR(t testing.TB) string { // 10 位 HLG（手机拍的 HDR 视频就是这种）
	return fixture(t, "HDR 视频.mp4", ffmpegCmd(
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=2",
		"-vf", "format=yuv420p10le", "-c:v", "libx265", "-preset", "ultrafast",
		"-x265-params", "log-level=error:colorprim=bt2020:transfer=arib-std-b67:colormatrix=bt2020nc",
		"-color_primaries", "bt2020", "-color_trc", "arib-std-b67", "-colorspace", "bt2020nc", "-f", "mp4"))
}

func fxSilent(t testing.TB) string { // 没有声音的视频
	return fixture(t, "无声 视频.mp4", ffmpegCmd(
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-f", "mp4"))
}

func fxWAV(t testing.TB) string { // 45 秒立体声 44.1 kHz
	return fixture(t, "测试 音乐.wav", ffmpegCmd(
		"-f", "lavfi", "-i", "sine=frequency=440:duration=45:sample_rate=44100",
		"-f", "lavfi", "-i", "anoisesrc=duration=45:color=pink:sample_rate=44100:amplitude=0.08",
		"-filter_complex", "[0][1]amerge=inputs=2", "-c:a", "pcm_s16le", "-f", "wav"))
}

func fxCoverMP3(t testing.TB) string { // 带专辑封面的 MP3
	cover := fixture(t, "封面.png", magickCmd("rose:", "-resize", "200x200!"))
	return fixture(t, "带 封面.mp3", ffmpegCmd(
		"-f", "lavfi", "-i", "sine=frequency=330:duration=8:sample_rate=44100", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c:a", "libmp3lame", "-b:a", "128k", "-c:v", "copy",
		"-disposition:v", "attached_pic", "-id3v2_version", "3", "-f", "mp3"))
}

// ---------------------------------------------------------------- 运行任务

type jobResult struct {
	outputs []string
	err     error
	notes   []string
	took    time.Duration
}

// runJob 像真正的执行者那样跑一个任务：输出到一个名字带中文和空格的新文件夹，跑完调用 Finish
func runJob(t testing.TB, run conv.RunFunc, in string, opt conv.Options) jobResult {
	t.Helper()
	out := filepath.Join(t.TempDir(), "输出 文件夹")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	return runJobIn(t, run, in, opt, out, context.Background())
}

func runJobIn(t testing.TB, run conv.RunFunc, in string, opt conv.Options, outDir string, ctx context.Context) jobResult {
	t.Helper()
	var mu sync.Mutex
	var res jobResult
	j := conv.NewJob([]string{in}, "test", opt, outDir, func(p conv.Progress) {
		if p.Frac > 1.0001 {
			t.Errorf("进度超过了 100%%：%v", p.Frac)
		}
		mu.Lock()
		if p.Note != "" && (len(res.notes) == 0 || res.notes[len(res.notes)-1] != p.Note) {
			res.notes = append(res.notes, p.Note)
		}
		mu.Unlock()
	})
	start := time.Now()
	res.err = run(ctx, j)
	res.took = time.Since(start)
	j.Finish(res.err != nil)
	for _, p := range j.Outputs() {
		if _, err := os.Stat(p); err == nil {
			res.outputs = append(res.outputs, p)
		}
	}
	return res
}

// one 要求任务成功并且只产生一个文件
func (r jobResult) one(t testing.TB) string {
	t.Helper()
	if r.err != nil {
		t.Fatalf("转换失败：%v", r.err)
	}
	if len(r.outputs) != 1 {
		t.Fatalf("应该输出 1 个文件，实际是 %v", r.outputs)
	}
	if fileSize(r.outputs[0]) == 0 {
		t.Fatalf("输出文件是空的：%s", r.outputs[0])
	}
	return r.outputs[0]
}

// userErr 要求任务失败，返回给用户看的错误，并且没有留下输出文件
func (r jobResult) userErr(t testing.TB) *conv.UserError {
	t.Helper()
	if r.err == nil {
		t.Fatalf("应该失败，却成功了：%v", r.outputs)
	}
	var ue *conv.UserError
	if !errors.As(r.err, &ue) {
		t.Fatalf("错误应该是 UserError，实际是 %T：%v", r.err, r.err)
	}
	if len(r.outputs) != 0 {
		t.Fatalf("失败后不该留下文件：%v", r.outputs)
	}
	return ue
}

// ---------------------------------------------------------------- 检查结果

type imgStat struct {
	format     string
	w, h       int
	frames     int
	colorspace string
}

func identify(t testing.TB, p string) imgStat {
	t.Helper()
	out, err := tools.Run(context.Background(), tools.Magick(), []string{"identify", "-quiet", "-format", "%m;%w;%h;%[colorspace]\n", p}, nil)
	if err != nil {
		t.Fatalf("读不出结果图片 %s：%v", p, err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "")), "\n")
	f := strings.Split(lines[0], ";")
	if len(f) < 4 {
		t.Fatalf("identify 输出不对：%q", out)
	}
	w, _ := strconv.Atoi(f[1])
	h, _ := strconv.Atoi(f[2])
	return imgStat{format: f[0], w: w, h: h, frames: len(lines), colorspace: f[3]}
}

// pixel 读某个像素，返回 srgba(...) 形式
func pixel(t testing.TB, p string, x, y int) string {
	t.Helper()
	out, err := tools.Run(context.Background(), tools.Magick(), []string{p + "[0]", "-format", fmt.Sprintf("%%[pixel:p{%d,%d}]", x, y), "info:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func probe(t testing.TB, p string) *Info {
	t.Helper()
	info, err := Probe(context.Background(), p)
	if err != nil {
		t.Fatalf("读不出结果 %s：%v", p, err)
	}
	return info
}

func near(a, b, tol float64) bool { return a >= b-tol && a <= b+tol }

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{500 * 1024: "500 KB", 50 * 1024: "50 KB", 1536 * 1024: "1.5 MB", 1 << 20: "1 MB", 1234: "1.21 KB"}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q，应该是 %q", n, got, want)
		}
	}
}

func TestPlanGeometry(t *testing.T) {
	cases := []struct {
		r          resizeSpec
		w, h       int
		even       bool
		outW, outH int
	}{
		{resizeSpec{Mode: "box", W: 413, H: 579, Fit: "cover"}, 1200, 800, false, 413, 579},
		{resizeSpec{Mode: "box", W: 1080, H: 1080, Fit: "cover"}, 1200, 800, false, 1080, 1080},
		{resizeSpec{Mode: "box", W: 500, H: 500, Fit: "contain"}, 1200, 800, false, 500, 333},
		{resizeSpec{Mode: "box", W: 500, H: 500, Fit: "pad"}, 1200, 800, false, 500, 500},
		{resizeSpec{Mode: "box", W: 300, H: 300, Fit: "stretch"}, 1200, 800, false, 300, 300},
		{resizeSpec{Mode: "box", W: 600}, 1200, 800, false, 600, 400},
		{resizeSpec{Mode: "box", H: 200}, 1200, 800, false, 300, 200},
		{resizeSpec{Mode: "percent", Percent: 50}, 1200, 800, false, 600, 400},
		{resizeSpec{Mode: "long", Long: 600}, 800, 1200, false, 400, 600},
		{resizeSpec{Mode: "long", Long: 2000}, 1200, 800, false, 1200, 800},
		{resizeSpec{Mode: "box", W: 413, H: 579, Fit: "cover"}, 1280, 720, true, 414, 580},
		{resizeSpec{Mode: "box", W: 720, H: 720, Fit: "cover"}, 1280, 720, true, 720, 720},
		{resizeSpec{Mode: "box", W: 1001, Fit: "contain"}, 1280, 720, true, 1002, 564},
	}
	for _, c := range cases {
		g := c.r.plan(c.w, c.h, c.even)
		if w, h := g.Out(); w != c.outW || h != c.outH {
			t.Errorf("%+v 作用在 %d×%d 上得到 %d×%d（%+v），应该是 %d×%d", c.r, c.w, c.h, w, h, g, c.outW, c.outH)
		}
		if c.r.Fit == "cover" && (g.SW < g.CW || g.SH < g.CH) {
			t.Errorf("裁剪前的尺寸 %d×%d 比裁剪框还小", g.SW, g.SH)
		}
	}
	if parseColor("#FFF") != "#ffffff" || parseColor("12ab34") != "#12ab34" || parseColor("abc!") != "" || parseColor("透明") != "none" {
		t.Error("parseColor 不对")
	}
	r := parseResize(conv.Options{conv.OptWidth: "800"})
	if r.Mode != "box" || r.W != 800 {
		t.Errorf("只填宽度应该当成指定宽高：%+v", r)
	}
}

func TestCancel(t *testing.T) {
	needTools(t)
	// 一开始就取消：马上返回 ErrCancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		run conv.RunFunc
		in  string
	}{
		"图片": {Image("png"), fxPhoto(t)},
		"视频": {Video("mp4"), fxShort(t)},
		"音频": {Audio("mp3"), fxWAV(t)},
	} {
		out := filepath.Join(t.TempDir(), "输出")
		os.MkdirAll(out, 0o755)
		if r := runJobIn(t, c.run, c.in, nil, out, ctx); !errors.Is(r.err, conv.ErrCancelled) || len(r.outputs) != 0 {
			t.Errorf("%s：应该返回 ErrCancelled，实际 %v %v", name, r.err, r.outputs)
		}
	}
	// 压缩图片到指定大小的过程中取消
	big := fxBigPhoto(t)
	out := filepath.Join(t.TempDir(), "输出 图片")
	os.MkdirAll(out, 0o755)
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(700*time.Millisecond, cancel)
	start := time.Now()
	r := runJobIn(t, Image("avif"), big, conv.Options{conv.OptTargetSize: "30000"}, out, ctx)
	if !errors.Is(r.err, conv.ErrCancelled) {
		t.Errorf("应该返回 ErrCancelled，实际 %v", r.err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("取消后过了 %v 才返回", time.Since(start))
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("取消后留下了文件：%v", entries)
	}
}
