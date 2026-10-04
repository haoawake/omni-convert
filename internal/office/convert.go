package office

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 每一类文件具体怎么转。

func baseName(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// wordSave 用文字处理程序（Word / WPS 文字 / LibreOffice）打开 src，另存为 to 格式写到 out
func (t *task) wordSave(src, out, to string, o openOpts) error {
	return withEngine(appWord, func(e engine) error {
		tmp, err := t.tempPath("out", "."+to)
		if err != nil {
			return err
		}
		if err := e.save(t, src, tmp, to, o); err != nil {
			return err
		}
		if err := checkOutput(tmp, e.name()); err != nil {
			return err
		}
		if o.web && to == "docx" {
			stripLinkedImages(tmp) // 网页里的图片只保留嵌入的那份，不指向临时文件
		}
		switch to {
		case "html":
			return inlineHTMLFile(tmp, out) // 图片嵌进网页，只输出一个文件
		case "txt":
			return ensureBOM(tmp, out)
		}
		return moveFile(tmp, out)
	})
}

// ---------------------------------------------------------------- Word

func (t *task) wordConvert(in, out, to string) error {
	src, err := t.copyIn(in, officeExt(in, appWord))
	if err != nil {
		return err
	}
	return t.wordSave(src, out, to, openOpts{})
}

// ---------------------------------------------------------------- PDF

func (t *task) pdfToDocx(in, out string) error {
	src, err := t.copyIn(in, ".pdf")
	if err != nil {
		return err
	}
	t.report(-1, "正在识别 PDF 的内容…")
	return t.wordSave(src, out, "docx", openOpts{pdf: true})
}

func (t *task) pdfToXlsx(in, out string) error {
	tmp, err := t.tempPath("pdf", ".docx")
	if err != nil {
		return err
	}
	if err := t.pdfToDocx(in, tmp); err != nil {
		return err
	}
	t.report(-1, "正在提取表格…")
	tables, paras, err := readDocx(tmp)
	if err != nil {
		return conv.Fail("没法读取识别结果", err.Error())
	}
	n, err := writeTablesXlsx(tables, paras, out)
	if err != nil {
		var ue *conv.UserError
		if errors.As(err, &ue) {
			return err
		}
		return conv.Fail("没法生成 Excel 文件", err.Error())
	}
	if n == 0 {
		t.report(1, "PDF 里没有找到表格，已把文字按段落放进 A 列（每段一行）")
	} else {
		t.report(1, "找到 "+itoa(n)+" 个表格，每个表格放在一个工作表里")
	}
	return nil
}

// ---------------------------------------------------------------- Excel

func (t *task) excelConvert(in, out, to string) error {
	if to == "html" {
		f, err := t.openWorkbook(in)
		if err != nil {
			return err
		}
		defer f.Close()
		return sheetsToHTML(t, f, out, baseName(in))
	}
	src, err := t.copyIn(in, officeExt(in, appExcel))
	if err != nil {
		return err
	}
	return t.excelSave(src, out, to, openOpts{})
}

func (t *task) excelSave(src, out, to string, o openOpts) error {
	return withEngine(appExcel, func(e engine) error {
		tmp, err := t.tempPath("out", "."+to)
		if err != nil {
			return err
		}
		if err := e.save(t, src, tmp, to, o); err != nil {
			return err
		}
		if err := checkOutput(tmp, e.name()); err != nil {
			return err
		}
		return moveFile(tmp, out)
	})
}

// excelizeReadable 判断能不能不用 Office 直接读（xlsx / xlsm 等 OOXML 格式）
func excelizeReadable(in string) bool {
	switch conv.Ext(in) {
	case ".xlsx", ".xlsm", ".xltx", ".xltm":
		return sniff(in) == "zip" && zipKind(in) != ".xlsb"
	}
	return false
}

// openWorkbook 打开表格给纯 Go 的代码读：OOXML 直接读，其他格式（xls、ods、et、xlsb…）先用 Excel 转成 xlsx
func (t *task) openWorkbook(in string) (*excelize.File, error) {
	if excelizeReadable(in) {
		f, err := openXlsx(in)
		if err == nil {
			return f, nil
		}
		if len(enginesFor(appExcel)) == 0 {
			return nil, conv.Fail("表格文件打不开，可能已经损坏", err.Error())
		}
	} else if sniff(in) == "ole" && strings.HasPrefix(conv.Ext(in), ".xlsx") && len(enginesFor(appExcel)) == 0 {
		return nil, conv.Fail("这个文件有打开密码，没法转换", "")
	}
	src, err := t.copyIn(in, officeExt(in, appExcel))
	if err != nil {
		return nil, err
	}
	tmp, err := t.tempPath("book", ".xlsx")
	if err != nil {
		return nil, err
	}
	if err := t.excelSave(src, tmp, "xlsx", openOpts{}); err != nil {
		return nil, err
	}
	f, err := openXlsx(tmp)
	if err != nil {
		return nil, conv.Fail("没法读取表格", err.Error())
	}
	return f, nil
}

// excelToCSV 每个非空工作表导出一个 CSV（只有一个时就叫「文件名.csv」，多个时叫「文件名-工作表名.csv」）
func (t *task) excelToCSV(in string, j *conv.Job) error {
	f, err := t.openWorkbook(in)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeSheetsCSV(t, f, func(sheet string, multi bool) string {
		if multi {
			return j.OutFileNamed(j.Base()+"-"+sheet, ".csv")
		}
		return j.OutFile(".csv")
	})
}

// ---------------------------------------------------------------- CSV

func (t *task) csvConvert(in, out, to string) error {
	switch to {
	case "csv":
		return reencodeCSV(in, out)
	case "xlsx":
		return csvToXlsx(t, in, out)
	}
	tmp, err := t.tempPath("csv", ".xlsx")
	if err != nil {
		return err
	}
	if err := csvToXlsx(t, in, tmp); err != nil {
		return err
	}
	if to == "html" {
		f, err := openXlsx(tmp)
		if err != nil {
			return conv.Fail("没法读取表格", err.Error())
		}
		defer f.Close()
		return sheetsToHTML(t, f, out, baseName(in))
	}
	return t.excelSave(tmp, out, to, openOpts{gridlines: true})
}

// ---------------------------------------------------------------- PowerPoint

func (t *task) pptConvert(in, out, to string) error {
	src, err := t.copyIn(in, officeExt(in, appPPT))
	if err != nil {
		return err
	}
	if sniff(src) == "zip" {
		stripModifyPassword(src) // 有修改密码时 PowerPoint 不让另存
	}
	if to == "mp4" {
		if !hasCOM(appPPT) || comProg(appPPT).vendor != vendorMS {
			return conv.Fail("PPT 转视频需要安装 Microsoft PowerPoint", "WPS 和 LibreOffice 不能导出视频")
		}
		ve, ok := newCOMEngine(appPPT).(videoEngine)
		if !ok {
			return conv.Fail("PPT 转视频需要安装 Microsoft PowerPoint", "")
		}
		sec := min(max(t.opt.Int(conv.OptSlideSec, 5), 1), 600)
		tmp, err := t.tempPath("out", ".mp4")
		if err != nil {
			return err
		}
		if err := ve.video(t, src, tmp, sec); err != nil {
			return err
		}
		if err := checkOutput(tmp, "PowerPoint"); err != nil {
			return err
		}
		return moveFile(tmp, out)
	}
	return withEngine(appPPT, func(e engine) error {
		tmp, err := t.tempPath("out", "."+to)
		if err != nil {
			return err
		}
		if err := e.save(t, src, tmp, to, openOpts{}); err != nil {
			return err
		}
		if err := checkOutput(tmp, e.name()); err != nil {
			return err
		}
		return moveFile(tmp, out)
	})
}

// ---------------------------------------------------------------- 纯文本

func (t *task) textConvert(in, out, to string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return conv.Fail("没法读取文件", err.Error())
	}
	text, _ := decodeText(raw)
	if to == "html" {
		return writeOut(out, textHTML(baseName(in), text))
	}
	if to == "pdf" && !hasCOM(appWord) && edgePath() != "" {
		page, err := t.tempPath("text", ".html")
		if err != nil {
			return err
		}
		if err := os.WriteFile(page, textHTML(baseName(in), text), 0o644); err != nil {
			return err
		}
		return t.edgePDF(page, out)
	}
	// 交给 Word / LibreOffice 前统一转成带 BOM 的 UTF-8，编码问题在这里一次解决
	src, err := t.tempPath("src", ".txt")
	if err != nil {
		return err
	}
	if err := writeUTF8BOM(src, crlf(text)); err != nil {
		return err
	}
	return t.wordSave(src, out, to, openOpts{codepage: 65001, text: true})
}

func writeOut(out string, b []byte) error {
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	return nil
}

// ---------------------------------------------------------------- Markdown

func (t *task) markdownConvert(in, out, to string) error {
	switch to {
	case "html":
		page, err := renderMarkdown(in, imgEmbed, false, "")
		if err != nil {
			return conv.Fail("没法读取 Markdown 文件", err.Error())
		}
		return writeOut(out, page)
	case "pdf":
		if edge := edgePath(); edge != "" {
			page, err := renderMarkdown(in, imgFileURL, false, "")
			if err != nil {
				return conv.Fail("没法读取 Markdown 文件", err.Error())
			}
			p, err := t.tempPath("md", ".html")
			if err != nil {
				return err
			}
			if err := os.WriteFile(p, page, 0o644); err != nil {
				return err
			}
			err = t.edgePDF(p, out)
			if err == nil || t.cancelled() || len(enginesFor(appWord)) == 0 {
				return err
			}
		}
	}
	// Word：打开专门为它准备的网页（图片复制到网页旁边），另存为 docx / pdf
	dir, err := t.tempDir("mdword")
	if err != nil {
		return err
	}
	page, err := renderMarkdown(in, imgCopy, true, dir)
	if err != nil {
		return conv.Fail("没法读取 Markdown 文件", err.Error())
	}
	p := filepath.Join(dir, "page.html")
	if err := os.WriteFile(p, page, 0o644); err != nil {
		return err
	}
	return t.wordSave(p, out, to, openOpts{web: true})
}

// ---------------------------------------------------------------- 网页

func (t *task) htmlConvert(in, out, to string) error {
	switch to {
	case "pdf":
		// Word 存的 mht（单个文件网页）Edge 显示会乱码，有 Word 时交给 Word
		mht := conv.Ext(in) == ".mht" || conv.Ext(in) == ".mhtml"
		if edgePath() != "" && !(mht && hasCOM(appWord)) {
			p, err := t.printableHTML(in)
			if err != nil {
				return err
			}
			err = t.edgePDF(p, out)
			if err == nil || t.cancelled() || len(enginesFor(appWord)) == 0 {
				return err
			}
		}
	case "txt":
		if len(enginesFor(appWord)) == 0 {
			s, err := htmlToText(in)
			if err != nil {
				return conv.Fail("没法读取网页", err.Error())
			}
			return writeUTF8BOM(out, crlf(s))
		}
	}
	// 网页直接从原位置打开（相对路径的图片才能找到），Word 以只读方式打开，不会改动它
	return t.wordSave(in, out, to, openOpts{web: true})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
