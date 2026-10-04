package office

import (
	"bytes"
	"context"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// 用 Edge 的无界面模式把网页打印成 PDF（Windows 10/11 都自带 Edge）。

func edgeArgs(profile, page, out string) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		"--disable-background-networking",
		"--disable-component-update",
		"--mute-audio",
		"--hide-scrollbars",
		"--user-data-dir=" + profile, // 一次性的配置文件夹，不碰用户自己的 Edge
		"--no-pdf-header-footer",
		"--print-to-pdf-no-header",
		"--print-to-pdf=" + out,
		fileURL(page),
	}
}

// edgePDF 把本地网页 page 打印成 PDF 写到 out
func (t *task) edgePDF(page, out string) error {
	edge := edgePath()
	if edge == "" {
		return conv.Fail("没有找到 Microsoft Edge", "")
	}
	profile, err := t.tempDir("edge")
	if err != nil {
		return err
	}
	tmp, err := t.tempPath("print", ".pdf")
	if err != nil {
		return err
	}
	t.report(-1, "正在用 Edge 生成 PDF…")
	ctx, cancel := context.WithTimeout(t.ctx, 3*time.Minute)
	defer cancel()
	_, runErr := tools.Run(ctx, edge, edgeArgs(profile, page, tmp), nil)
	if t.cancelled() {
		return conv.ErrCancelled
	}
	if !isPDFFile(tmp) {
		detail := ""
		if runErr != nil {
			detail = runErr.Error()
		}
		if ctx.Err() != nil {
			detail = "超过 3 分钟没有完成"
		}
		return conv.Fail("Edge 没能生成 PDF", detail)
	}
	return moveFile(tmp, out)
}

var reZeroPages = regexp.MustCompile(`/Type\s*/Pages\s*/Count\s+0\b`)

// pdfHasNoPages 判断 PDF 是不是一页都没有（只看开头部分，Office 把页面目录写在前面）
func pdfHasNoPages(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 256*1024)
	n, _ := io.ReadFull(f, head)
	return reZeroPages.Match(head[:n])
}

func isPDFFile(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 5)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, []byte("%PDF-"))
}
