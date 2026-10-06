// Package office 负责文档类的转换：Word、Excel、PowerPoint、纯文本、Markdown、网页、CSV，
// 以及 PDF 转 Word / Excel。
//
// Windows 上能用 Microsoft Office 就用 Office（COM 自动化，效果最好），其次 WPS，再次 LibreOffice；
// macOS 上用 LibreOffice（Office for Mac 不能在后台自动转换）。
// CSV ↔ Excel、Markdown → 网页这些不需要 Office，用纯 Go 完成；网页、Markdown 转 PDF 优先用 Edge（macOS 上也可以是 Chrome）。
package office

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/haoawake/omni-convert/internal/conv"
)

// Info 说明这台电脑上能用什么程序转换
type Info struct {
	Word, Excel, PowerPoint string // "Microsoft Office" | "WPS" | "LibreOffice" | ""
	LibreOffice, Edge       string // 程序路径，没有时为空（Edge 在 macOS 上也可以是 Chrome，用来把网页打印成 PDF）
}

var (
	detectMu      sync.Mutex
	detectDone    bool
	detected      Info
	detectedProgs [numApps]progInfo
)

// Detect 查一下装了哪些软件（只看注册表或者固定的安装位置，不启动程序；结果会缓存）
func Detect() Info {
	detectMu.Lock()
	defer detectMu.Unlock()
	if !detectDone {
		detectDone = true
		detected, detectedProgs = detectSystem()
		for _, p := range []*string{&detected.Word, &detected.Excel, &detected.PowerPoint} {
			if *p == "" && detected.LibreOffice != "" {
				*p = vendorLO
			}
		}
	}
	return detected
}

// Refresh 让下一次 Detect 重新查找（用户刚装好 LibreOffice 回到程序时，不用重启就能用）
func Refresh() {
	detectMu.Lock()
	detectDone = false
	detectMu.Unlock()
}

func comProg(k appKind) progInfo {
	Detect()
	return detectedProgs[k]
}

// HasWord 判断有没有能处理 Word 文档的程序（Microsoft Word、WPS 文字或 LibreOffice 都算）
func HasWord() bool { return Detect().Word != "" }

// Shutdown 退出所有我们启动的 Office 程序（程序退出时调用，可以重复调用）
func Shutdown() { shutdownCOM() }

// ---------------------------------------------------------------- 能转成什么

var targets = map[conv.Family][]string{
	conv.FamWord:     {"pdf", "docx", "doc", "rtf", "odt", "txt", "html"},
	conv.FamExcel:    {"pdf", "xlsx", "xls", "csv", "ods", "html"},
	conv.FamCSV:      {"xlsx", "xls", "ods", "pdf", "html", "csv"},
	conv.FamPPT:      {"pdf", "pptx", "ppt", "odp", "mp4"},
	conv.FamText:     {"pdf", "docx", "html"},
	conv.FamMarkdown: {"html", "pdf", "docx"},
	conv.FamHTML:     {"pdf", "docx", "txt"},
}

var pdfTargets = []string{"docx", "xlsx"}

func normTarget(to string) string {
	to = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(to), "."))
	switch to {
	case "htm":
		return "html"
	case "text":
		return "txt"
	}
	return to
}

// CanConvert 判断这种文件能不能转成 to（只看文件类型，不管装没装软件；界面用来把按钮变灰）
func CanConvert(in, to string) bool {
	to = normTarget(to)
	list := targets[conv.FamilyOf(in)]
	if conv.IsPDF(in) {
		list = pdfTargets
	}
	for _, t := range list {
		if t == to {
			return true
		}
	}
	return false
}

// Targets 列出这种文件能转成的格式
func Targets(in string) []string {
	if conv.IsPDF(in) {
		return append([]string(nil), pdfTargets...)
	}
	return append([]string(nil), targets[conv.FamilyOf(in)]...)
}

// ---------------------------------------------------------------- 对外的转换入口

// Convert 是文档页的「转成 xx」：to 是 pdf docx doc rtf odt txt html xlsx xls csv ods pptx ppt odp mp4 之一
func Convert(to string) conv.RunFunc {
	to = normTarget(to)
	return func(ctx context.Context, j *conv.Job) error {
		in := j.Input()
		if !CanConvert(in, to) {
			return conv.Fail(fmt.Sprintf("%s 文件不能转成 %s", upper(conv.Ext(in)), upper(to)), "")
		}
		if err := checkInput(in); err != nil {
			return err
		}
		t := newTask(ctx, j)
		defer t.close()
		var err error
		if to == "csv" && conv.FamilyOf(in) == conv.FamExcel {
			err = t.excelToCSV(in, j)
		} else {
			err = t.convert(in, j.OutFile("."+to), to)
		}
		if err != nil && ctx.Err() != nil {
			return conv.ErrCancelled
		}
		return err
	}
}

// ToPDF 把任意支持的文档转成 PDF，写到 out（给「文档转图片」用）
func ToPDF(ctx context.Context, in, out string) error {
	if err := checkInput(in); err != nil {
		return err
	}
	if conv.IsPDF(in) {
		return copyFile(in, out)
	}
	if !CanConvert(in, "pdf") {
		return conv.Fail(upper(conv.Ext(in))+" 文件不能转成 PDF", "")
	}
	t := newTask(ctx, nil)
	defer t.close()
	err := t.convert(in, out, "pdf")
	if err != nil {
		os.Remove(out)
		if ctx != nil && ctx.Err() != nil {
			return conv.ErrCancelled
		}
	}
	return err
}

// PDFToWord 把 PDF 转成可以编辑的 Word 文档（Word 的「PDF 重排」；没有 Word 时用 LibreOffice）
func PDFToWord() conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		return runPDF(ctx, j, "docx")
	}
}

// PDFToExcel 把 PDF 里的表格提取到 Excel：每个表格一个工作表；没有表格时每段文字一行
func PDFToExcel() conv.RunFunc {
	return func(ctx context.Context, j *conv.Job) error {
		return runPDF(ctx, j, "xlsx")
	}
}

func runPDF(ctx context.Context, j *conv.Job, to string) error {
	in := j.Input()
	if !conv.IsPDF(in) {
		return conv.Fail("这不是 PDF 文件", "")
	}
	if err := checkInput(in); err != nil {
		return err
	}
	t := newTask(ctx, j)
	defer t.close()
	err := t.convert(in, j.OutFile("."+to), to)
	if err != nil && ctx.Err() != nil {
		return conv.ErrCancelled
	}
	return err
}

// checkInput 确认输入文件存在、能读
func checkInput(in string) error {
	fi, err := os.Stat(in)
	if err != nil {
		return conv.Fail("找不到文件", in)
	}
	if fi.IsDir() {
		return conv.Fail("这是一个文件夹，不是文件", in)
	}
	f, err := os.Open(in)
	if err != nil {
		return conv.Fail("文件打不开，可能正被别的程序独占", err.Error())
	}
	f.Close()
	return nil
}

// convert 把 in 转成 to 格式，写到 out
func (t *task) convert(in, out, to string) error {
	if conv.IsPDF(in) {
		switch to {
		case "docx":
			return t.pdfToDocx(in, out)
		case "xlsx":
			return t.pdfToXlsx(in, out)
		}
	}
	switch conv.FamilyOf(in) {
	case conv.FamWord:
		return t.wordConvert(in, out, to)
	case conv.FamExcel:
		return t.excelConvert(in, out, to)
	case conv.FamCSV:
		return t.csvConvert(in, out, to)
	case conv.FamPPT:
		return t.pptConvert(in, out, to)
	case conv.FamText:
		return t.textConvert(in, out, to)
	case conv.FamMarkdown:
		return t.markdownConvert(in, out, to)
	case conv.FamHTML:
		return t.htmlConvert(in, out, to)
	}
	return conv.Fail("不支持这种文件", "")
}

// CanExportVideo 判断能不能用 Microsoft PowerPoint 把演示文稿导出成视频（保留动画和切换效果）
func CanExportVideo() bool { return hasCOM(appPPT) && comProg(appPPT).vendor == vendorMS }
