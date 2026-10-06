package office

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// 用 Edge 的无界面模式把网页打印成 PDF（Windows 10/11 都自带 Edge；macOS 上用 Edge 或 Chrome，参数一样）。

// browserName 是打印用的浏览器的名字，显示在进度和错误里
func browserName(p string) string {
	low := strings.ToLower(filepath.Base(p))
	switch {
	case strings.Contains(low, "edge"):
		return "Edge"
	case strings.Contains(low, "chrom"):
		return "Chrome"
	}
	return "浏览器"
}

func edgeArgs(profile, page, out string) []string {
	args := []string{
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
	}
	if runtime.GOOS == "darwin" {
		args = append(args, "--use-mock-keychain") // 不去碰钥匙串，免得弹出「想要访问钥匙串」
	}
	return append(args, fileURL(page))
}

// edgePDF 把本地网页 page 打印成 PDF 写到 out
func (t *task) edgePDF(page, out string) error {
	edge := edgePath()
	if edge == "" {
		return conv.Fail("没有找到 Microsoft Edge", "")
	}
	name := browserName(edge)
	profile, err := t.tempDir("edge")
	if err != nil {
		return err
	}
	tmp, err := t.tempPath("print", ".pdf")
	if err != nil {
		return err
	}
	t.report(-1, "正在用 "+name+" 生成 PDF…")
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
		return conv.Fail(name+" 没能生成 PDF", detail)
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
