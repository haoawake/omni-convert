package office

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

// LibreOffice：没有 Office / WPS 时的备选，用命令行 soffice --headless --convert-to。

type sofficeEngine struct{ exe string }

func (sofficeEngine) name() string { return "LibreOffice" }

// 目标格式 → --convert-to 参数（扩展名:导出过滤器[:选项]）
var loFilters = map[string]string{
	"pdf":  "pdf",
	"docx": "docx:MS Word 2007 XML",
	"doc":  "doc:MS Word 97",
	"rtf":  "rtf:Rich Text Format",
	"odt":  "odt",
	"txt":  "txt:Text (encoded):UTF8",
	"html": "html",
	"xlsx": "xlsx:Calc MS Excel 2007 XML",
	"xls":  "xls:MS Excel 97",
	"ods":  "ods",
	"pptx": "pptx:Impress MS PowerPoint 2007 XML",
	"ppt":  "ppt:MS PowerPoint 97",
	"odp":  "odp",
}

// 同一个用户配置文件夹同时只能有一个 LibreOffice 在用，所以排队
var loMu sync.Mutex

// loArgs 生成命令行参数
func loArgs(profile, in, outDir, format string, o openOpts) []string {
	args := []string{
		"-env:UserInstallation=" + fileURL(profile),
		"--headless", "--invisible", "--norestore", "--nologo", "--nodefault", "--nolockcheck", "--nofirststartwizard",
	}
	switch {
	case o.pdf:
		args = append(args, "--infilter=writer_pdf_import")
	case o.text:
		args = append(args, "--infilter=Text (encoded):UTF8")
	}
	return append(args, "--convert-to", loFilters[format], "--outdir", outDir, in)
}

func loProfile() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "omni-convert", "libreoffice")
}

func (e sofficeEngine) save(t *task, in, out, format string, o openOpts) error {
	if _, ok := loFilters[format]; !ok {
		return conv.Fail("LibreOffice 不能存成 "+upper(format), "")
	}
	exe := e.exe
	// soffice.com 是控制台版本，会等转换结束再退出
	if c := strings.TrimSuffix(exe, filepath.Ext(exe)) + ".com"; fileExists(c) {
		exe = c
	}
	outDir, err := t.tempDir("lo")
	if err != nil {
		return err
	}
	t.report(-1, "正在用 LibreOffice 转换…")
	loMu.Lock()
	defer loMu.Unlock()
	timeout := 10 * time.Minute
	if o.pdf {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	_, runErr := tools.Run(ctx, exe, loArgs(loProfile(), in, outDir, format, o), nil)
	if t.cancelled() {
		return conv.ErrCancelled
	}
	if ctx.Err() != nil {
		return conv.Fail("LibreOffice 转换超时了", "")
	}
	want := filepath.Join(outDir, baseName(in)+"."+format)
	if !fileExists(want) {
		// 有的过滤器输出的扩展名不一样，取文件夹里唯一的结果
		if ents, _ := os.ReadDir(outDir); len(ents) == 1 && !ents[0].IsDir() {
			want = filepath.Join(outDir, ents[0].Name())
		}
	}
	if !fileExists(want) {
		detail := ""
		if runErr != nil {
			detail = runErr.Error()
			if errors.Is(runErr, conv.ErrCancelled) {
				return runErr
			}
		}
		return conv.Fail("LibreOffice 没法转换这个文件", detail)
	}
	if format == "html" {
		return inlineHTMLFile(want, out)
	}
	return moveFile(want, out)
}
