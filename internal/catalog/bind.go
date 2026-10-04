//go:build !stub

package catalog

// 把每个按钮接到真正干活的转换器上

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/media"
	"github.com/haoawake/omni-convert/internal/office"
	"github.com/haoawake/omni-convert/internal/pdf"
)

var gpuReady atomic.Bool

// DetectGPU 在后台检测显卡编码器，检测完调用 done（可以为 nil）。程序启动时调用一次。
func DetectGPU(done func()) {
	go func() {
		if len(media.GPUEncoders(context.Background())) > 0 {
			gpuReady.Store(true)
		}
		if done != nil {
			done()
		}
	}()
}

// OfficeInfo 说明文档转换用的是哪个软件，显示在文档页上
func OfficeInfo() string {
	in := office.Detect()
	var apps []string
	if in.Word != "" {
		apps = append(apps, in.Word)
	}
	if in.LibreOffice != "" && in.Word != "LibreOffice" {
		apps = append(apps, "LibreOffice")
	}
	if len(apps) == 0 {
		return ""
	}
	return strings.Join(apps, "、")
}

// Shutdown 程序退出前关掉我们打开的 Word、Excel、PowerPoint
func Shutdown() { office.Shutdown() }

func bindAll() {
	GPUAvailable = gpuReady.Load
	for _, t := range PageOf(conv.Image).Targets {
		id := strings.TrimPrefix(t.ID, "img:")
		switch id {
		case "pdf":
			t.Run = imagesToPDF(false)
		case "pdfmerge":
			t.Run = imagesToPDF(true)
		default:
			t.Run = media.Image(id)
		}
	}
	for _, t := range PageOf(conv.Video).Targets {
		t.Run = media.Video(strings.TrimPrefix(t.ID, "vid:"))
	}
	for _, t := range PageOf(conv.Audio).Targets {
		t.Run = media.Audio(strings.TrimPrefix(t.ID, "aud:"))
	}
	for _, t := range PageOf(conv.Doc).Targets {
		id := strings.TrimPrefix(t.ID, "doc:")
		switch id {
		case "png", "long":
			t.Run = docViaPDF(id)
		default:
			t.Run = office.Convert(id)
		}
	}
	pdfRuns := map[string]conv.RunFunc{
		"docx":     pdfToWord,
		"xlsx":     office.PDFToExcel(),
		"pptx":     pdf.ToPPT(),
		"jpg":      pdf.ToImages("jpg"),
		"png":      pdf.ToImages("png"),
		"long":     pdf.ToLongImage(),
		"txt":      pdf.ToText(),
		"merge":    pdf.Merge(),
		"split":    pdf.Split(),
		"compress": pdf.Compress(),
		"encrypt":  pdf.Encrypt(),
		"decrypt":  pdf.Decrypt(),
		"rotate":   pdf.Rotate(),
	}
	for _, t := range PageOf(conv.PDF).Targets {
		t.Run = pdfRuns[strings.TrimPrefix(t.ID, "pdf:")]
	}
}

// imagesToPDF 先把每张图片整理成 PDF 能直接放进去的 JPG/PNG，再写成 PDF
func imagesToPDF(merge bool) conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		tmp, err := j.TempDir()
		if err != nil {
			return err
		}
		inputs := j.Inputs
		var ready []string
		for i, in := range inputs {
			if ctx.Err() != nil {
				return conv.ErrCancelled
			}
			if len(inputs) > 1 {
				j.Report(float64(i)/float64(len(inputs)), fmt.Sprintf("正在处理第 %d/%d 张", i+1, len(inputs)))
			}
			p, err := media.PrepareForPDF(ctx, in, j.Opt, tmp)
			if err != nil {
				if len(inputs) > 1 {
					return fmt.Errorf("%s：%w", filepath.Base(in), err)
				}
				return err
			}
			ready = append(ready, p)
		}
		var out string
		if merge && len(inputs) > 1 {
			out = j.OutFileNamed(fmt.Sprintf("%s 等%d张图片", j.Base(), len(inputs)), ".pdf")
		} else {
			out = j.OutFile(".pdf")
		}
		j.Report(0.95, "正在生成 PDF")
		return pdf.WriteImagesPDF(out, ready, j.Opt)
	}
}

// docViaPDF 先用 Office 转成 PDF，再把 PDF 画成图片或长图
func docViaPDF(to string) conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		tmp, err := j.TempDir()
		if err != nil {
			return err
		}
		src := filepath.Join(tmp, "文档.pdf")
		j.Report(-1, "正在转成 PDF…")
		if err := office.ToPDF(ctx, j.Input(), src); err != nil {
			return err
		}
		if to == "png" {
			return pdf.ImagesFrom(ctx, j, src, "png")
		}
		return pdf.LongImageFrom(ctx, j, src)
	}
}

// pdfToWord 有 Word（或 LibreOffice）时转成可编辑的文档，都没有时只提取文字
func pdfToWord(ctx context.Context, j *conv.Job) error {
	in := office.Detect()
	if in.Word != "" || in.LibreOffice != "" {
		return office.PDFToWord()(ctx, j)
	}
	j.Report(-1, "没有找到 Word，只提取文字")
	return pdf.ToDocxText()(ctx, j)
}
