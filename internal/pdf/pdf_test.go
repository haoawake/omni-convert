package pdf

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 测试用的文件都放在带中文和空格的路径里
var (
	fxDir       string
	textPDF     string // 3 页中文文字 PDF（Edge 生成）
	photoOnce   sync.Once
	photoPDF    string // 3 页大照片 PDF（2 张 JPEG + 1 张 PNG）
	photoErr    error
	textPage1   = "春眠不觉晓，处处闻啼鸟。"
	textPage2   = "床前明月光，疑是地上霜。"
	textPage3   = "白日依山尽，黄河入海流。"
	testPW      = "密码 Abc123"
	fixtureName = "中文 测试文档"
)

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "PDF 测试 *")
	if err != nil {
		panic(err)
	}
	fxDir = filepath.Join(d, "素材 文件夹")
	os.MkdirAll(fxDir, 0o755)
	b, err := os.ReadFile(filepath.Join("testdata", fixtureName+".pdf"))
	if err != nil {
		panic(err)
	}
	textPDF = filepath.Join(fxDir, fixtureName+".pdf")
	os.WriteFile(textPDF, b, 0o644)
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}

func needPdfium(t *testing.T) {
	t.Helper()
	if err := pdfiumAvailable(); err != nil {
		t.Skip("没有 pdfium：", err)
	}
}

// synthPhoto 生成一张像照片的图（平滑的颜色变化 + 噪点），JPEG 压不了太小
func synthPhoto(w, h int, seed int64) *image.RGBA {
	r := rand.New(rand.NewSource(seed))
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := float64(y) / float64(h)
		for x := 0; x < w; x++ {
			fx := float64(x) / float64(w)
			n := func() float64 { return float64(r.Intn(25) - 12) }
			cr := 128 + 90*math.Sin(fx*7+fy*3) + n()
			cg := 128 + 90*math.Sin(fx*2-fy*6+1) + n()
			cb := 128 + 90*math.Cos(fx*4+fy*5) + n()
			i := m.PixOffset(x, y)
			m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = clamp8(cr), clamp8(cg), clamp8(cb), 255
		}
	}
	return m
}

func clamp8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v))) }

func writeJPEG(t testing.TB, path string, m image.Image, q int) {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePNG(t testing.TB, path string, m image.Image) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// getPhotoPDF 用 WriteImagesPDF 生成一份「大照片」PDF（只生成一次）
func getPhotoPDF(t *testing.T) string {
	t.Helper()
	photoOnce.Do(func() {
		dir := filepath.Join(fxDir, "照片 素材")
		os.MkdirAll(dir, 0o755)
		a := filepath.Join(dir, "照片 1.jpg")
		b := filepath.Join(dir, "照片 2.jpg")
		c := filepath.Join(dir, "截图 3.png")
		writeJPEG(t, a, synthPhoto(4000, 3000, 1), 95)
		writeJPEG(t, b, synthPhoto(3000, 4000, 2), 92)
		writePNG(t, c, synthPhoto(1800, 1200, 3))
		photoPDF = filepath.Join(fxDir, "大照片 文档.pdf")
		photoErr = WriteImagesPDF(photoPDF, []string{a, b, c}, conv.Options{conv.OptPageSize: "a4"})
	})
	if photoErr != nil {
		t.Fatal(photoErr)
	}
	return photoPDF
}

// testJob 建一个任务，输出到带中文和空格的文件夹里
type testJob struct {
	*conv.Job
	mu    sync.Mutex
	notes []string
}

func newJob(t *testing.T, inputs []string, opt conv.Options) *testJob {
	t.Helper()
	out := filepath.Join(t.TempDir(), "输出 目录")
	os.MkdirAll(out, 0o755)
	tj := &testJob{}
	tj.Job = conv.NewJob(inputs, "pdf:test", opt, out, func(p conv.Progress) {
		tj.mu.Lock()
		tj.notes = append(tj.notes, p.Note)
		tj.mu.Unlock()
	})
	return tj
}

func (tj *testJob) lastNote() string {
	tj.mu.Lock()
	defer tj.mu.Unlock()
	if len(tj.notes) == 0 {
		return ""
	}
	return tj.notes[len(tj.notes)-1]
}

func run(t *testing.T, f conv.RunFunc, tj *testJob) error {
	t.Helper()
	return runCtx(context.Background(), f, tj)
}

func runCtx(ctx context.Context, f conv.RunFunc, tj *testJob) error {
	err := f(ctx, tj.Job)
	tj.Finish(err != nil)
	return err
}

func mustRun(t *testing.T, f conv.RunFunc, tj *testJob) {
	t.Helper()
	if err := run(t, f, tj); err != nil {
		t.Fatalf("任务失败：%v", err)
	}
}

// outputs 列出输出文件夹里的东西（不含子文件夹内容）
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func pageCount(t *testing.T, path, pw string) int {
	t.Helper()
	n, err := PageCount(path, pw)
	if err != nil {
		t.Fatalf("打不开 %s：%v", filepath.Base(path), err)
	}
	return n
}

func docText(t *testing.T, path, pw string, page int) string {
	t.Helper()
	d, err := Open(path, pw)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	return d.Text(page)
}

func userMsg(err error) string {
	var ue *conv.UserError
	if errors.As(err, &ue) {
		return ue.Msg
	}
	if err == nil {
		return ""
	}
	return "非用户错误：" + err.Error()
}

// encryptedFixture 用 Encrypt 生成一份加密的文字 PDF
func encryptedFixture(t *testing.T) string {
	t.Helper()
	tj := newJob(t, []string{textPDF}, conv.Options{conv.OptPassword: testPW})
	mustRun(t, Encrypt(), tj)
	out := tj.Outputs()[0]
	if filepath.Base(out) != fixtureName+"_加密.pdf" {
		t.Fatalf("加密输出文件名不对：%s", out)
	}
	return out
}

// ---------------------------------------------------------------- pdfium 基础

func TestPageCountAndChinesePath(t *testing.T) {
	needPdfium(t)
	if n := pageCount(t, textPDF, ""); n != 3 {
		t.Fatalf("页数 %d，应该是 3", n)
	}
	// 更深、更怪的中文路径
	p := filepath.Join(t.TempDir(), "一级 目录", "二级（括号）& 符号", "文件 名 #1.pdf")
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := os.ReadFile(textPDF)
	os.WriteFile(p, b, 0o644)
	if n := pageCount(t, p, ""); n != 3 {
		t.Fatalf("中文路径页数 %d", n)
	}
	if n, err := pdfcpuPageCount(p, ""); err != nil || n != 3 {
		t.Fatalf("pdfcpu 页数 %d %v", n, err)
	}
}

func TestRenderDims(t *testing.T) {
	needPdfium(t)
	d, err := Open(textPDF, "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	wPt, hPt := d.PageSize(0)
	if math.Abs(wPt-595) > 2 || math.Abs(hPt-842) > 2 {
		t.Fatalf("A4 页面尺寸不对：%v×%v", wPt, hPt)
	}
	for _, dpi := range []float64{72, 150, 300} {
		st := time.Now()
		img, err := d.Render(0, dpi)
		if err != nil {
			t.Fatal(err)
		}
		el := time.Since(st)
		ew, eh := pixelSize(wPt, hPt, dpi/72)
		if img.Bounds().Dx() != ew || img.Bounds().Dy() != eh {
			t.Fatalf("%v dpi 尺寸 %v，应该是 %d×%d", dpi, img.Bounds(), ew, eh)
		}
		t.Logf("%v dpi：%d×%d，用时 %v", dpi, ew, eh, el)
		// 左上角是白的，页面里有深色文字
		if c := img.RGBAAt(2, 2); c != (color.RGBA{255, 255, 255, 255}) {
			t.Fatalf("背景不是白色：%v", c)
		}
		dark := 0
		for i := 0; i < len(img.Pix); i += 4 {
			if img.Pix[i] < 80 && img.Pix[i+1] < 80 && img.Pix[i+2] < 80 {
				dark++
			}
		}
		if dark < 100 {
			t.Fatalf("没画出文字（深色像素 %d）", dark)
		}
	}
	if s := d.Text(1); !strings.Contains(s, textPage2) {
		t.Fatalf("第 2 页文字不对：%q", s)
	}
	// 越界
	if _, err := d.Render(5, 72); err == nil {
		t.Fatal("页码越界应该报错")
	}
}

func TestClampPixels(t *testing.T) {
	w, h := clampPixels(100000, 50)
	if w > maxSide || h < 1 {
		t.Fatalf("%d×%d", w, h)
	}
	w, h = clampPixels(15000, 15000)
	if w*h > maxPixels || w != h {
		t.Fatalf("%d×%d", w, h)
	}
	if w, h := clampPixels(800, 600); w != 800 || h != 600 {
		t.Fatalf("不该缩小：%d×%d", w, h)
	}
}

func TestCorruptPDF(t *testing.T) {
	needPdfium(t)
	p := filepath.Join(t.TempDir(), "坏掉的 文件.pdf")
	os.WriteFile(p, []byte("这不是 PDF 文件，只是一些文字。"), 0o644)
	_, err := Open(p, "")
	if userMsg(err) != "PDF 文件已损坏，打不开" {
		t.Fatalf("错误信息：%v", err)
	}
	tj := newJob(t, []string{p}, nil)
	if err := run(t, ToImages("png"), tj); userMsg(err) != "PDF 文件已损坏，打不开" {
		t.Fatalf("转图片的错误信息：%v", err)
	}
	if len(listDir(t, tj.OutDir)) != 0 {
		t.Fatal("失败了不应该留下输出")
	}
	// 截断一半的 PDF：pdfium 能修就修，修不了要给出中文错误
	b, _ := os.ReadFile(textPDF)
	half := filepath.Join(t.TempDir(), "截断 文件.pdf")
	os.WriteFile(half, b[:len(b)/2], 0o644)
	if d, err := Open(half, ""); err == nil {
		t.Logf("截断的文件还能打开，%d 页", d.PageCount())
		d.Close()
	} else if _, ok := err.(*conv.UserError); !ok {
		t.Fatalf("不是给用户看的错误：%v", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "不存在.pdf"), ""); userMsg(err) != "找不到这个文件" {
		t.Fatalf("文件不存在的错误：%v", err)
	}
}

// ---------------------------------------------------------------- 密码

func TestEncryptDecrypt(t *testing.T) {
	needPdfium(t)
	enc := encryptedFixture(t)

	_, err := Open(enc, "")
	if err != errNeedPassword {
		t.Fatalf("没填密码：%v", err)
	}
	_, err = Open(enc, "错误的密码")
	if err != errWrongPassword {
		t.Fatalf("密码错误：%v", err)
	}
	if n := pageCount(t, enc, testPW); n != 3 {
		t.Fatalf("加密后页数 %d", n)
	}
	if s := docText(t, enc, testPW, 0); !strings.Contains(s, textPage1) {
		t.Fatalf("加密后文字：%q", s)
	}

	// 加密的文件转图片：没密码给出友好提示，有密码正常
	tj := newJob(t, []string{enc}, nil)
	if err := run(t, ToImages("png"), tj); userMsg(err) != "这个 PDF 有打开密码，请在「密码」里填上" {
		t.Fatalf("没密码转图片：%v", err)
	}
	tj = newJob(t, []string{enc}, conv.Options{conv.OptPassword: testPW, conv.OptPages: "1", conv.OptDPI: "50"})
	mustRun(t, ToImages("png"), tj)

	// 解密
	tj = newJob(t, []string{enc}, conv.Options{conv.OptPassword: "不对"})
	if err := run(t, Decrypt(), tj); err != errWrongPassword {
		t.Fatalf("错误密码解密：%v", err)
	}
	tj = newJob(t, []string{enc}, nil)
	if err := run(t, Decrypt(), tj); err != errNeedPassword {
		t.Fatalf("不填密码解密：%v", err)
	}
	tj = newJob(t, []string{enc}, conv.Options{conv.OptPassword: testPW})
	mustRun(t, Decrypt(), tj)
	dec := tj.Outputs()[0]
	if n := pageCount(t, dec, ""); n != 3 {
		t.Fatalf("解密后页数 %d", n)
	}
	if s := docText(t, dec, "", 2); !strings.Contains(s, textPage3) {
		t.Fatalf("解密后文字：%q", s)
	}
	d, _ := Open(dec, "")
	if d.Encrypted() {
		t.Fatal("解密后还是加密的")
	}
	d.Close()

	// 没加密的文件解密
	tj = newJob(t, []string{textPDF}, nil)
	if err := run(t, Decrypt(), tj); userMsg(err) != "这个 PDF 没有设置密码，不需要解除" {
		t.Fatalf("未加密文件解密：%v", err)
	}
	// 没填密码就加密
	tj = newJob(t, []string{textPDF}, nil)
	if err := run(t, Encrypt(), tj); userMsg(err) != "请先填上要设置的密码" {
		t.Fatalf("没密码加密：%v", err)
	}
	// 已经加密的再加密
	tj = newJob(t, []string{enc}, conv.Options{conv.OptPassword: "新密码"})
	if err := run(t, Encrypt(), tj); err != errAlreadyEncrypted {
		t.Fatalf("重复加密：%v", err)
	}
}

// ---------------------------------------------------------------- 转图片

func TestToImagesFolder(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, conv.Options{conv.OptDPI: "72"})
	mustRun(t, ToImages("png"), tj)
	dir := filepath.Join(tj.OutDir, fixtureName)
	got := listDir(t, dir)
	want := []string{fixtureName + "_001.png", fixtureName + "_002.png", fixtureName + "_003.png"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("文件：%v", got)
	}
	f, _ := os.Open(filepath.Join(dir, want[1]))
	cfg, err := png.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != 595 || cfg.Height != 842 {
		t.Fatalf("72 dpi 图片尺寸 %d×%d %v", cfg.Width, cfg.Height, err)
	}
}

func TestToImagesSinglePageAndJPG(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, conv.Options{conv.OptPages: "2", conv.OptDPI: "100"})
	mustRun(t, ToImages("jpg"), tj)
	got := listDir(t, tj.OutDir)
	if len(got) != 1 || got[0] != fixtureName+".jpg" {
		t.Fatalf("单页输出：%v", got)
	}
	f, _ := os.Open(filepath.Join(tj.OutDir, got[0]))
	cfg, err := jpeg.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != 826 {
		t.Fatalf("100 dpi 宽度 %d %v", cfg.Width, err)
	}

	// 页码范围
	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptPages: "3,1", conv.OptDPI: "40"})
	mustRun(t, ToImages("jpg"), tj)
	got = listDir(t, filepath.Join(tj.OutDir, fixtureName))
	if strings.Join(got, "|") != fixtureName+"_001.jpg|"+fixtureName+"_003.jpg" {
		t.Fatalf("范围输出：%v", got)
	}
	// 范围超出
	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptPages: "9-12"})
	if err := run(t, ToImages("png"), tj); !strings.Contains(userMsg(err), "超出了总页数") {
		t.Fatalf("超出范围：%v", err)
	}
}

func TestImagesFrom(t *testing.T) {
	needPdfium(t)
	// 模拟 Word 转图片：输入是 docx，实际渲染的是临时 PDF
	docx := filepath.Join(t.TempDir(), "季度 报告.docx")
	os.WriteFile(docx, []byte("x"), 0o644)
	tj := newJob(t, []string{docx}, conv.Options{conv.OptDPI: "30"})
	err := ImagesFrom(context.Background(), tj.Job, textPDF, "png")
	tj.Finish(err != nil)
	if err != nil {
		t.Fatal(err)
	}
	got := listDir(t, filepath.Join(tj.OutDir, "季度 报告"))
	if len(got) != 3 || got[0] != "季度 报告_001.png" {
		t.Fatalf("输出：%v", got)
	}
}

func TestCancel(t *testing.T) {
	needPdfium(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tj := newJob(t, []string{textPDF}, nil)
	if err := runCtx(ctx, ToImages("png"), tj); !errors.Is(err, conv.ErrCancelled) {
		t.Fatalf("提前取消：%v", err)
	}
	// 处理到一半取消：输出要被清理掉
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	out := filepath.Join(t.TempDir(), "输出")
	os.MkdirAll(out, 0o755)
	n := 0
	j := conv.NewJob([]string{getPhotoPDF(t)}, "pdf:png", conv.Options{conv.OptDPI: "150"}, out, func(p conv.Progress) {
		n++
		if n == 2 {
			cancel()
		}
	})
	err := ToImages("png")(ctx, j)
	j.Finish(err != nil)
	if !errors.Is(err, conv.ErrCancelled) {
		t.Fatalf("中途取消：%v", err)
	}
	if es, _ := os.ReadDir(out); len(es) != 0 {
		t.Fatalf("取消后还有输出：%v", es)
	}
	for _, f := range []conv.RunFunc{ToLongImage(), ToPPT(), ToText(), Compress(), Merge(), Split()} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tj := newJob(t, []string{textPDF, textPDF}, conv.Options{conv.OptLevel: "strong"})
		if err := runCtx(ctx, f, tj); !errors.Is(err, conv.ErrCancelled) {
			t.Fatalf("取消后应该返回 ErrCancelled：%v", err)
		}
	}
}

// ---------------------------------------------------------------- 长图

func jpegSize(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

func TestLongImage(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, ToLongImage(), tj)
	got := listDir(t, tj.OutDir)
	if len(got) != 1 || got[0] != fixtureName+"_长图.jpg" {
		t.Fatalf("长图输出：%v", got)
	}
	w, h := jpegSize(t, filepath.Join(tj.OutDir, got[0]))
	ph := int(math.Round(1080 * 841.92 / 594.96))
	if w != 1080 || abs(h-(3*ph+2*9)) > 3 {
		t.Fatalf("长图尺寸 %d×%d，应该约 1080×%d", w, h, 3*ph+18)
	}
	// 解码看看：页与页之间是浅灰色
	f, _ := os.Open(filepath.Join(tj.OutDir, got[0]))
	m, err := jpeg.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := m.At(540, ph+4).RGBA()
	if r>>8 < 0xD0 || r>>8 > 0xF0 || abs(int(r>>8)-int(g>>8)) > 4 || abs(int(g>>8)-int(b>>8)) > 4 {
		t.Fatalf("间隔颜色 %d %d %d", r>>8, g>>8, b>>8)
	}
	r, _, _, _ = m.At(540, 5).RGBA()
	if r>>8 < 0xF5 {
		t.Fatalf("页面背景不是白色：%d", r>>8)
	}
}

func TestLongImageSplit(t *testing.T) {
	needPdfium(t)
	// 50 页 A4：1080 宽时总高约 76800，超过 JPEG 上限，要拆成两张
	dir := t.TempDir()
	img := filepath.Join(dir, "小图.png")
	m := image.NewRGBA(image.Rect(0, 0, 60, 80))
	for i := range m.Pix {
		m.Pix[i] = uint8(i * 7)
	}
	writePNG(t, img, m)
	imgs := make([]string, 50)
	for i := range imgs {
		imgs[i] = img
	}
	many := filepath.Join(dir, "五十 页.pdf")
	if err := WriteImagesPDF(many, imgs, conv.Options{conv.OptPageSize: "a4"}); err != nil {
		t.Fatal(err)
	}
	st := time.Now()
	tj := newJob(t, []string{many}, conv.Options{conv.OptLongW: "1080"})
	mustRun(t, ToLongImage(), tj)
	t.Logf("50 页长图用时 %v", time.Since(st))
	got := listDir(t, tj.OutDir)
	if len(got) != 2 || got[0] != "五十 页_长图1.jpg" || got[1] != "五十 页_长图2.jpg" {
		t.Fatalf("拆分输出：%v", got)
	}
	total := 0
	for _, g := range got {
		w, h := jpegSize(t, filepath.Join(tj.OutDir, g))
		if w != 1080 || h > 65535 {
			t.Fatalf("%s：%d×%d", g, w, h)
		}
		total += h
	}
	ph := int(math.Round(1080 * a4H / a4W))
	if abs(total-(50*ph+48*9)) > 60 {
		t.Fatalf("总高度 %d，应该约 %d", total, 50*ph+48*9)
	}
	// 宽 4000：每张不超过 3 万像素高
	tj = newJob(t, []string{many}, conv.Options{conv.OptLongW: "4000", conv.OptPages: "1-12"})
	mustRun(t, ToLongImage(), tj)
	got = listDir(t, tj.OutDir)
	if len(got) < 2 {
		t.Fatalf("宽图应该拆开：%v", got)
	}
	for _, g := range got {
		w, h := jpegSize(t, filepath.Join(tj.OutDir, g))
		if w != 4000 || h > 30000 {
			t.Fatalf("%s：%d×%d", g, w, h)
		}
	}
}

func TestPlanLong(t *testing.T) {
	// 一页本身超长时只能从中间切
	parts := planLong([][2]int{{100, 500}, {100, 2500}, {100, 300}}, 10, 1000)
	var hs []int
	for _, p := range parts {
		if p.h > 1000 {
			t.Fatalf("超出上限：%d", p.h)
		}
		hs = append(hs, p.h)
	}
	if fmt.Sprint(hs) != "[500 1000 1000 500 300]" && fmt.Sprint(hs) != "[500 1000 1000 810]" {
		t.Fatalf("切分：%v", hs)
	}
}

// ---------------------------------------------------------------- 文字、Word、PPT

func TestToText(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, ToText(), tj)
	b, err := os.ReadFile(filepath.Join(tj.OutDir, fixtureName+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "\xEF\xBB\xBF") {
		t.Fatal("没有 BOM")
	}
	for _, want := range []string{textPage1, textPage2, textPage3, "—— 第 2 页 ——\r\n", "—— 第 3 页 ——", "The quick brown fox"} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺少 %q：\n%s", want, s)
		}
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Fatal("有不是 CRLF 的换行")
	}
	if strings.Contains(s, "第 1 页") {
		t.Fatal("第一页前面不该有分隔线")
	}
	// 扫描件（只有图片）
	tj = newJob(t, []string{getPhotoPDF(t)}, nil)
	if err := run(t, ToText(), tj); err != errNoText {
		t.Fatalf("没有文字的 PDF：%v", err)
	}
}

func TestParagraphs(t *testing.T) {
	text := "标题\n" +
		"这是一段很长很长的中文文字，排版的时候被自动换行拆开了，所以第一行会一直写到页面的最右\n" +
		"边，然后接着写到第二行，这一行也写得满满的满满的满满的满满的满满的满满的满满的满满\n" +
		"的。\n" +
		"1. 第一项\n" +
		"2. 第二项\n" +
		"\n" +
		"An English paragraph that is long enough to be wrapped by the layout engine of the conver-\n" +
		"sion tool and should be joined back together into a single paragraph by our heuristic.\n" +
		"Short line."
	ps := paragraphs(text)
	want := []string{
		"标题",
		"这是一段很长很长的中文文字，排版的时候被自动换行拆开了，所以第一行会一直写到页面的最右边，然后接着写到第二行，这一行也写得满满的满满的满满的满满的满满的满满的满满的满满的。",
		"1. 第一项",
		"2. 第二项",
		"An English paragraph that is long enough to be wrapped by the layout engine of the conversion tool and should be joined back together into a single paragraph by our heuristic.",
		"Short line.",
	}
	if strings.Join(ps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("段落：\n%s", strings.Join(ps, "\n"))
	}
}

func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	m := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		m[f.Name] = string(b)
	}
	return m
}

func TestToDocxText(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, ToDocxText(), tj)
	out := filepath.Join(tj.OutDir, fixtureName+".docx")
	files := readZip(t, out)
	doc := files["word/document.xml"]
	if doc == "" || files["[Content_Types].xml"] == "" || files["word/styles.xml"] == "" {
		t.Fatalf("docx 缺少部件：%v", len(files))
	}
	if !strings.Contains(doc, "被自动换行拆开的句子重新拼接起来") || !strings.Contains(doc, "寒来暑往") {
		t.Fatal("自动换行的句子没有拼回去")
	}
	if c := strings.Count(doc, "<w:pageBreakBefore/>"); c != 2 {
		t.Fatalf("分页 %d 个", c)
	}
	if !strings.Contains(doc, textPage2) {
		t.Fatal("缺少第 2 页文字")
	}
	checkOOXML(t, files)
}

func TestToPPT(t *testing.T) {
	needPdfium(t)
	st := time.Now()
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, ToPPT(), tj)
	t.Logf("3 页转 PPT 用时 %v", time.Since(st))
	out := filepath.Join(tj.OutDir, fixtureName+".pptx")
	files := readZip(t, out)
	for i := 1; i <= 3; i++ {
		if files[fmt.Sprintf("ppt/slides/slide%d.xml", i)] == "" || files[fmt.Sprintf("ppt/media/image%d.jpg", i)] == "" {
			t.Fatalf("缺少第 %d 页", i)
		}
	}
	if !strings.Contains(files["ppt/notesSlides/notesSlide2.xml"], textPage2) {
		t.Fatal("备注里没有文字")
	}
	// A4 纵向：约 595×842 点 = 7556500×10693400 EMU
	var cx, cy int
	pres := files["ppt/presentation.xml"]
	if i := strings.Index(pres, "<p:sldSz "); i < 0 {
		t.Fatal("没有 sldSz")
	} else {
		fmt.Sscanf(pres[i:], `<p:sldSz cx="%d" cy="%d"`, &cx, &cy)
	}
	if abs(cx-7556500) > 10000 || abs(cy-10693400) > 10000 {
		t.Fatalf("幻灯片大小 %d×%d", cx, cy)
	}
	m, err := jpeg.Decode(strings.NewReader(files["ppt/media/image1.jpg"]))
	if err != nil || m.Bounds().Dy() < 2000 {
		t.Fatalf("幻灯片图片 %v %v", m.Bounds(), err)
	}
	checkOOXML(t, files)
	if cx, cy := slideSize(10000, 50); cx > slideMax || cy < slideMin {
		t.Fatalf("超大比例：%d %d", cx, cy)
	}
}

// ---------------------------------------------------------------- 合并、拆分、旋转

func TestMerge(t *testing.T) {
	needPdfium(t)
	photo := getPhotoPDF(t)
	second := filepath.Join(t.TempDir(), "第二 份.pdf")
	b, _ := os.ReadFile(textPDF)
	os.WriteFile(second, b, 0o644)
	tj := newJob(t, []string{textPDF, photo, second}, nil)
	mustRun(t, Merge(), tj)
	out := tj.Outputs()[0]
	if filepath.Base(out) != fixtureName+"等3个文件_合并.pdf" {
		t.Fatalf("文件名：%s", filepath.Base(out))
	}
	if n := pageCount(t, out, ""); n != 9 {
		t.Fatalf("合并后 %d 页", n)
	}
	for page, want := range map[int]string{0: textPage1, 2: textPage3, 6: textPage1, 7: textPage2} {
		if s := docText(t, out, "", page); !strings.Contains(s, want) {
			t.Fatalf("第 %d 页内容不对：%q", page+1, s)
		}
	}
	if s := docText(t, out, "", 3); strings.TrimSpace(s) != "" {
		t.Fatalf("第 4 页应该是照片：%q", s)
	}
	// 书签：每个原文件一项
	f, _ := os.Open(out)
	bms, err := api.Bookmarks(context.Background(), f, nil)
	f.Close()
	if err != nil || len(bms) != 3 || bms[0].Title != fixtureName || bms[1].Title != "大照片 文档" || bms[2].PageFrom != 7 {
		t.Fatalf("书签不对：%+v %v", bms, err)
	}

	// 有一个文件加了密：填了密码就能合并
	enc := encryptedFixture(t)
	tj = newJob(t, []string{textPDF, enc}, nil)
	if err := run(t, Merge(), tj); !strings.Contains(userMsg(err), "密码") || !strings.Contains(userMsg(err), "_加密.pdf") {
		t.Fatalf("加密文件合并：%v", err)
	}
	tj = newJob(t, []string{enc, textPDF}, conv.Options{conv.OptPassword: testPW})
	mustRun(t, Merge(), tj)
	if n := pageCount(t, tj.Outputs()[0], ""); n != 6 {
		t.Fatalf("合并加密文件后 %d 页", n)
	}
	// pdfium 兜底的合并
	tj = newJob(t, []string{textPDF, photo}, nil)
	out = tj.OutFileNamed("兜底", ".pdf")
	err = mergePdfium(context.Background(), tj.Job, "", out)
	tj.Finish(err != nil)
	if err != nil || pageCount(t, out, "") != 6 {
		t.Fatalf("pdfium 合并：%v", err)
	}
	// 只有一个文件
	tj = newJob(t, []string{textPDF}, nil)
	if err := run(t, Merge(), tj); err == nil {
		t.Fatal("一个文件不能合并")
	}
}

func TestSplit(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, Split(), tj)
	dir := filepath.Join(tj.OutDir, fixtureName+"_拆分")
	got := listDir(t, dir)
	if strings.Join(got, "|") != fixtureName+"_第1页.pdf|"+fixtureName+"_第2页.pdf|"+fixtureName+"_第3页.pdf" {
		t.Fatalf("每页一个：%v", got)
	}
	for i, g := range got {
		p := filepath.Join(dir, g)
		if pageCount(t, p, "") != 1 {
			t.Fatalf("%s 不是 1 页", g)
		}
		want := []string{textPage1, textPage2, textPage3}[i]
		if s := docText(t, p, "", 0); !strings.Contains(s, want) {
			t.Fatalf("%s 内容不对", g)
		}
	}

	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptSplit: "every", conv.OptEvery: "2"})
	mustRun(t, Split(), tj)
	got = listDir(t, filepath.Join(tj.OutDir, fixtureName+"_拆分"))
	if strings.Join(got, "|") != fixtureName+"_1-2页.pdf|"+fixtureName+"_第3页.pdf" {
		t.Fatalf("每 2 页：%v", got)
	}

	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptSplit: "ranges", conv.OptRanges: "1，2-3"})
	mustRun(t, Split(), tj)
	got = listDir(t, filepath.Join(tj.OutDir, fixtureName+"_拆分"))
	if strings.Join(got, "|") != fixtureName+"_2-3页.pdf|"+fixtureName+"_第1页.pdf" {
		t.Fatalf("按范围：%v", got)
	}

	// 只有一段：直接输出一个文件（提取页面）
	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptSplit: "ranges", conv.OptRanges: "2-3"})
	mustRun(t, Split(), tj)
	got = listDir(t, tj.OutDir)
	if len(got) != 1 || got[0] != fixtureName+"_2-3页.pdf" {
		t.Fatalf("提取页面：%v", got)
	}
	p := filepath.Join(tj.OutDir, got[0])
	if pageCount(t, p, "") != 2 || !strings.Contains(docText(t, p, "", 0), textPage2) {
		t.Fatal("提取的页面不对")
	}

	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptSplit: "ranges"})
	if err := run(t, Split(), tj); !strings.Contains(userMsg(err), "页码范围") {
		t.Fatalf("没填范围：%v", err)
	}
	// 加密文件拆分
	enc := encryptedFixture(t)
	tj = newJob(t, []string{enc}, conv.Options{conv.OptPassword: testPW, conv.OptSplit: "every", conv.OptEvery: "3"})
	mustRun(t, Split(), tj)
	if got := listDir(t, tj.OutDir); len(got) != 1 {
		t.Fatalf("加密文件拆分：%v", got)
	}
}

func TestRotate(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, conv.Options{conv.OptAngle: "90", conv.OptPages: "1,3"})
	mustRun(t, Rotate(), tj)
	out := tj.Outputs()[0]
	if filepath.Base(out) != fixtureName+"_旋转.pdf" {
		t.Fatalf("文件名 %s", out)
	}
	d, err := Open(out, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, landscape := range []bool{true, false, true} {
		w, h := d.PageSize(i)
		if (w > h) != landscape {
			t.Fatalf("第 %d 页 %v×%v", i+1, w, h)
		}
	}
	// 顺时针 90°：原来左上角的标题跑到右上角，渲染出来右边有字、左边空
	img, _ := d.Render(0, 50)
	d.Close()
	if dark(img, img.Rect.Dx()*3/4, img.Rect.Dx()) == 0 {
		t.Fatal("旋转方向不对：右侧应该有文字")
	}

	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptAngle: "180"})
	mustRun(t, Rotate(), tj)
	if w, h := pageSizeOf(t, tj.Outputs()[0], 1); w > h {
		t.Fatal("180° 不该变横向")
	}
	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptAngle: "45"})
	if err := run(t, Rotate(), tj); err == nil {
		t.Fatal("45° 应该报错")
	}
	// 加密文件旋转（带密码）
	enc := encryptedFixture(t)
	tj = newJob(t, []string{enc}, conv.Options{conv.OptAngle: "270", conv.OptPassword: testPW})
	mustRun(t, Rotate(), tj)
	if w, h := pageSizeOf(t, tj.Outputs()[0], 0, testPW); w < h {
		t.Fatal("加密文件没转过来")
	}
}

func pageSizeOf(t *testing.T, path string, i int, pw ...string) (float64, float64) {
	t.Helper()
	p := ""
	if len(pw) > 0 {
		p = pw[0]
	}
	d, err := Open(path, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	return d.PageSize(i)
}

// dark 数一数 x0~x1 列里的深色像素
func dark(img *image.RGBA, x0, x1 int) int {
	n := 0
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := x0; x < x1; x++ {
			c := img.RGBAAt(x, y)
			if c.R < 100 && c.G < 100 && c.B < 100 {
				n++
			}
		}
	}
	return n
}

// ---------------------------------------------------------------- 压缩

func TestCompress(t *testing.T) {
	needPdfium(t)
	photo := getPhotoPDF(t)
	before := fileSize(photo)
	t.Logf("照片 PDF：%s", conv.HumanSize(before))
	sizes := map[string]int64{}
	for _, level := range []string{"light", "medium", "strong"} {
		st := time.Now()
		tj := newJob(t, []string{photo}, conv.Options{conv.OptLevel: level})
		mustRun(t, Compress(), tj)
		out := tj.Outputs()[0]
		if filepath.Base(out) != "大照片 文档_压缩.pdf" {
			t.Fatalf("文件名 %s", out)
		}
		if n := pageCount(t, out, ""); n != 3 {
			t.Fatalf("%s：%d 页", level, n)
		}
		// 每页都能渲染
		d, _ := Open(out, "")
		for i := 0; i < 3; i++ {
			if _, err := d.Render(i, 20); err != nil {
				t.Fatal(err)
			}
		}
		w, h := d.PageSize(0)
		d.Close()
		if math.Abs(w-a4H) > 1 || math.Abs(h-a4W) > 1 {
			t.Fatalf("%s 页面尺寸变了：%v×%v", level, w, h)
		}
		sizes[level] = fileSize(out)
		note := tj.lastNote()
		t.Logf("%-6s %s → %s，%v，%s", level, conv.HumanSize(before), conv.HumanSize(sizes[level]), time.Since(st).Round(time.Millisecond), note)
		if !strings.Contains(note, "压缩到") && !strings.Contains(note, "已经很小了") {
			t.Fatalf("最后的提示：%q", note)
		}
	}
	if sizes["light"] > before {
		t.Fatal("无损压缩变大了")
	}
	if sizes["medium"] > before/3 || sizes["strong"] > before/3 {
		t.Fatalf("压缩得不够：%v", sizes)
	}

	// 文字 PDF 中等压缩后文字还在
	tj := newJob(t, []string{textPDF}, conv.Options{conv.OptLevel: "medium"})
	mustRun(t, Compress(), tj)
	if s := docText(t, tj.Outputs()[0], "", 1); !strings.Contains(s, textPage2) {
		t.Fatalf("压缩后文字没了：%q", s)
	}
	// 强力压缩小文件：压不下去就原样输出
	tj = newJob(t, []string{textPDF}, conv.Options{conv.OptLevel: "strong"})
	mustRun(t, Compress(), tj)
	t.Logf("文字 PDF 强力压缩：%s", tj.lastNote())
	if fileSize(tj.Outputs()[0]) > fileSize(textPDF) {
		t.Fatal("输出比原文件还大")
	}
	// 加密文件压缩
	enc := encryptedFixture(t)
	tj = newJob(t, []string{enc}, conv.Options{conv.OptLevel: "medium", conv.OptPassword: testPW})
	mustRun(t, Compress(), tj)
	if n := pageCount(t, tj.Outputs()[0], testPW); n != 3 {
		t.Fatalf("加密文件压缩后 %d 页", n)
	}
}

func TestDownscale(t *testing.T) {
	// 纯色缩小后还是纯色；渐变缩小后单调
	w, h := 101, 37
	src := make([]byte, w*h*3)
	for i := 0; i < len(src); i += 3 {
		src[i], src[i+1], src[i+2] = 200, 100, 50
	}
	dst := downscale(src, w, h, 3, 33, 10)
	for i := 0; i < len(dst); i += 3 {
		if dst[i] != 200 || dst[i+1] != 100 || dst[i+2] != 50 {
			t.Fatalf("纯色变了：%v", dst[i:i+3])
		}
	}
	g := make([]byte, 400)
	for i := range g {
		g[i] = uint8(i * 255 / 399)
	}
	out := downscale(g, 400, 1, 1, 7, 1)
	for i := 1; i < len(out); i++ {
		if out[i] <= out[i-1] {
			t.Fatalf("渐变不单调：%v", out)
		}
	}
}

// ---------------------------------------------------------------- 图片合成 PDF

// exifJPEG 生成一张带 EXIF 方向标记的 JPEG
func exifJPEG(t *testing.T, path string, m image.Image, orient int) {
	t.Helper()
	var b bytes.Buffer
	jpeg.Encode(&b, m, &jpeg.Options{Quality: 90})
	raw := b.Bytes()
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orient), 0, 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out := append([]byte{0xFF, 0xD8}, app1...)
	out = append(out, seg...)
	out = append(out, raw[2:]...)
	os.WriteFile(path, out, 0o644)
}

func TestWriteImagesPDF(t *testing.T) {
	needPdfium(t)
	dir := filepath.Join(t.TempDir(), "图片 素材")
	os.MkdirAll(dir, 0o755)

	// 左红右蓝 200×100
	rb := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= 100 {
				c = color.RGBA{20, 20, 220, 255}
			}
			rb.SetRGBA(x, y, c)
		}
	}
	rgbJ := filepath.Join(dir, "彩色.jpg")
	writeJPEG(t, rgbJ, rb, 90)
	gray := image.NewGray(image.Rect(0, 0, 120, 160))
	for i := range gray.Pix {
		gray.Pix[i] = uint8(i % 251)
	}
	grayJ := filepath.Join(dir, "灰度.jpg")
	writeJPEG(t, grayJ, gray, 90)
	rot := filepath.Join(dir, "旋转 90.jpg")
	exifJPEG(t, rot, rb, 6)
	// 半透明 PNG：上半透明、下半不透明的绿色
	na := image.NewNRGBA(image.Rect(0, 0, 80, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			a := uint8(255)
			if y < 40 {
				a = 0
			}
			na.SetNRGBA(x, y, color.NRGBA{30, 200, 60, a})
		}
	}
	alphaP := filepath.Join(dir, "透明.png")
	writePNG(t, alphaP, na)
	palP := filepath.Join(dir, "调色板.png")
	pal := image.NewPaletted(image.Rect(0, 0, 50, 50), color.Palette{color.White, color.RGBA{255, 0, 0, 255}})
	for i := range pal.Pix {
		pal.Pix[i] = uint8(i % 2)
	}
	writePNG(t, palP, pal)

	imgs := []string{rgbJ, grayJ, rot, alphaP, palP}
	cmyk := filepath.Join(dir, "印刷 CMYK.jpg")
	if _, err := os.Stat(magickPath()); err == nil {
		if err := runMagick(dir, "-size", "200x100", "xc:#DC1414", "-colorspace", "CMYK", "-quality", "90", cmyk); err == nil {
			imgs = append(imgs, cmyk)
		} else {
			t.Logf("生成 CMYK 图片失败：%v", err)
		}
	}

	t.Logf("测试 %d 张图片（含 CMYK：%v）", len(imgs), len(imgs) == 6)
	fit := filepath.Join(dir, "合成 fit.pdf")
	if err := WriteImagesPDF(fit, imgs, nil); err != nil {
		t.Fatal(err)
	}
	a4 := filepath.Join(dir, "合成 a4.pdf")
	if err := WriteImagesPDF(a4, imgs, conv.Options{conv.OptPageSize: "a4"}); err != nil {
		t.Fatal(err)
	}
	validateStrict(t, fit)
	validateStrict(t, a4)

	d, err := Open(fit, "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.PageCount() != len(imgs) {
		t.Fatalf("页数 %d", d.PageCount())
	}
	wants := [][2]float64{{150, 75}, {90, 120}, {75, 150}, {72, 72}, {72, 72}, {150, 75}}
	for i := range imgs {
		w, h := d.PageSize(i)
		if math.Abs(w-wants[i][0]) > 0.5 || math.Abs(h-wants[i][1]) > 0.5 {
			t.Fatalf("第 %d 页 %v×%v，应该是 %v", i+1, w, h, wants[i])
		}
	}
	px := func(page, x, y int) color.RGBA {
		img, err := d.Render(page, 96)
		if err != nil {
			t.Fatal(err)
		}
		return img.RGBAAt(x*img.Rect.Dx()/100, y*img.Rect.Dy()/100)
	}
	isRed := func(c color.RGBA) bool { return c.R > 160 && c.G < 90 && c.B < 90 }
	isBlue := func(c color.RGBA) bool { return c.B > 160 && c.R < 90 && c.G < 90 }
	if c := px(0, 25, 50); !isRed(c) {
		t.Fatalf("彩色图左边不是红色：%v", c)
	}
	if c := px(0, 75, 50); !isBlue(c) {
		t.Fatalf("彩色图右边不是蓝色：%v", c)
	}
	// EXIF 方向 6：顺时针转 90°，原来的左边（红）到了上面
	if c := px(2, 50, 25); !isRed(c) {
		t.Fatalf("旋转图上面不是红色：%v", c)
	}
	if c := px(2, 50, 75); !isBlue(c) {
		t.Fatalf("旋转图下面不是蓝色：%v", c)
	}
	// 透明 PNG：透明部分显示成白底，不透明部分是绿色
	if c := px(3, 50, 20); c.R < 240 || c.G < 240 || c.B < 240 {
		t.Fatalf("透明部分不是白色：%v", c)
	}
	if c := px(3, 50, 80); !(c.G > 150 && c.R < 80) {
		t.Fatalf("不透明部分不是绿色：%v", c)
	}
	if len(imgs) == 6 {
		if c := px(5, 50, 50); !isRed(c) {
			t.Fatalf("CMYK 图颜色不对（可能反相了）：%v", c)
		}
	}

	da, err := Open(a4, "")
	if err != nil {
		t.Fatal(err)
	}
	defer da.Close()
	for i, landscape := range []bool{true, false, false, false, false} {
		w, h := da.PageSize(i)
		ew, eh := a4W, a4H
		if landscape {
			ew, eh = a4H, a4W
		}
		if math.Abs(w-ew) > 0.5 || math.Abs(h-eh) > 0.5 {
			t.Fatalf("A4 第 %d 页 %v×%v", i+1, w, h)
		}
	}

	// 不支持的格式、坏图片
	bad := filepath.Join(dir, "坏图.jpg")
	os.WriteFile(bad, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 4, 1, 2}, 0o644)
	if err := WriteImagesPDF(filepath.Join(dir, "x.pdf"), []string{rgbJ, bad}, nil); !strings.Contains(userMsg(err), "坏图.jpg") {
		t.Fatalf("坏图片：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.pdf")); err == nil {
		t.Fatal("失败后不该留下文件")
	}
	gif := filepath.Join(dir, "动图.gif")
	os.WriteFile(gif, []byte("GIF89a......"), 0o644)
	if err := WriteImagesPDF(filepath.Join(dir, "y.pdf"), []string{gif}, nil); !strings.Contains(userMsg(err), "只支持") {
		t.Fatalf("GIF：%v", err)
	}
}

func TestOrientMatrix(t *testing.T) {
	// 每种方向下，单位正方形的四个角都应该落在目标矩形里
	for o := 1; o <= 8; o++ {
		a, b, c, d, e, f := orientMatrix(o, 10, 20, 30, 40)
		for _, uv := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
			x := a*uv[0] + c*uv[1] + e
			y := b*uv[0] + d*uv[1] + f
			if x < 10-1e-9 || x > 40+1e-9 || y < 20-1e-9 || y > 60+1e-9 {
				t.Fatalf("方向 %d：(%v,%v) → (%v,%v)", o, uv[0], uv[1], x, y)
			}
		}
	}
}

// ---------------------------------------------------------------- 不规范的文件

// brokenXref 把文字 PDF 的交叉引用表位置改坏（很多「能看不能改」的文件就是这样）
func brokenXref(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(textPDF)
	i := bytes.LastIndex(b, []byte("startxref"))
	if i < 0 {
		t.Fatal("没找到 startxref")
	}
	bad := append(append([]byte{}, b[:i]...), []byte("startxref\n123\n%%EOF\n")...)
	p := filepath.Join(t.TempDir(), "交叉引用 坏了.pdf")
	os.WriteFile(p, bad, 0o644)
	return p
}

func TestBrokenXref(t *testing.T) {
	needPdfium(t)
	bad := brokenXref(t)
	if n := pageCount(t, bad, ""); n != 3 {
		t.Fatalf("pdfium 应该能修好：%d 页", n)
	}
	for name, c := range map[string]struct {
		f   conv.RunFunc
		opt conv.Options
	}{
		"合并":  {Merge(), nil},
		"拆分":  {Split(), nil},
		"旋转":  {Rotate(), conv.Options{conv.OptAngle: "90"}},
		"加密":  {Encrypt(), conv.Options{conv.OptPassword: "1234"}},
		"压缩":  {Compress(), conv.Options{conv.OptLevel: "medium"}},
		"转图片": {ToImages("png"), conv.Options{conv.OptDPI: "30"}},
	} {
		inputs := []string{bad}
		if name == "合并" {
			inputs = []string{bad, textPDF}
		}
		tj := newJob(t, inputs, c.opt)
		if err := run(t, c.f, tj); err != nil {
			t.Fatalf("%s 失败：%v", name, err)
		}
		if len(tj.Outputs()) == 0 {
			t.Fatalf("%s 没有输出", name)
		}
	}
}

func TestWithRepair(t *testing.T) {
	needPdfium(t)
	tj := newJob(t, []string{textPDF}, nil)
	defer tj.Finish(false)
	var seen []string
	err := withRepair(context.Background(), tj.Job, textPDF, "", func(in, pw string) error {
		seen = append(seen, in)
		if in == textPDF {
			panic("pdfcpu 遇到怪文件崩了")
		}
		if n := pageCount(t, in, pw); n != 3 {
			return fmt.Errorf("修好的副本 %d 页", n)
		}
		return nil
	})
	if err != nil || len(seen) != 2 {
		t.Fatalf("兜底没起作用：%v %v", err, seen)
	}
	// 两次都失败：给出中文错误
	err = withRepair(context.Background(), tj.Job, textPDF, "", func(in, pw string) error { return errors.New("boom") })
	if userMsg(err) != "PDF 文件有问题，处理不了" {
		t.Fatalf("两次都失败：%v", err)
	}
	// guard 把 panic 变成中文错误
	err = guarded(func(ctx context.Context, j *conv.Job) error { panic("意外") })(context.Background(), tj.Job)
	if userMsg(err) != "处理这个 PDF 时出错了" {
		t.Fatalf("guard：%v", err)
	}
}
