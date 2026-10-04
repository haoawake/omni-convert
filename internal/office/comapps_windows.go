//go:build windows

package office

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
)

// Word / Excel / PowerPoint（以及对象模型相同的 WPS）的具体操作。这些函数都在 COM 线程上运行。

// bogusPW 是故意给的错误密码：有密码的文件会直接报错，而不是弹出输入密码的对话框把程序卡住
const bogusPW = "\x01omni"

type comEngine struct{ kind appKind }

func newCOMEngine(k appKind) engine { return comEngine{k} }

func (e comEngine) name() string { return comProg(e.kind).appName(e.kind) }

func (e comEngine) save(t *task, in, out, format string, o openOpts) error {
	timeout := 10 * time.Minute
	if o.pdf {
		timeout = 30 * time.Minute
	}
	err := runCOM(t, e.kind, timeout, func(a *comApp) error {
		switch e.kind {
		case appWord:
			return wordSave(t, a, in, out, format, o)
		case appExcel:
			return excelSave(t, a, in, out, format, o)
		default:
			return pptSave(t, a, in, out, format)
		}
	})
	return friendly(err, e.name())
}

// openError 表示打开文件这一步就失败了
type openError struct{ err error }

func (e *openError) Error() string { return e.err.Error() }
func (e *openError) Unwrap() error { return e.err }

var reTempPath = regexp.MustCompile(`[（(]?[A-Za-z]:\\[^()（）]*` + tempMarker + `[^()（）]*[)）]?`)

// friendly 把 COM 错误翻译成用户看得懂的话
func friendly(err error, app string) error {
	if err == nil {
		return nil
	}
	var ue *conv.UserError
	var un *unavailableError
	if errors.As(err, &ue) || errors.As(err, &un) || errors.Is(err, conv.ErrCancelled) {
		return err
	}
	ce, ok := asCOMError(err)
	if !ok {
		return conv.Fail(app+" 转换失败", err.Error())
	}
	desc := strings.TrimSpace(reTempPath.ReplaceAllString(ce.Desc, ""))
	if desc == "" {
		desc = fmt.Sprintf("错误码 0x%08X", ce.Code())
	}
	low := strings.ToLower(desc)
	var oe *openError
	switch {
	case isDead(err):
		return conv.Fail(app+" 意外退出了", desc)
	case strings.Contains(desc, "密码") || strings.Contains(low, "password") || ce.Code() == 0x800A1520:
		return conv.Fail("这个文件有打开密码，没法转换", "")
	case errors.As(err, &oe):
		return conv.Fail("文件打不开，可能已经损坏或者格式不对", desc)
	}
	return conv.Fail(app+" 没法转换这个文件", desc)
}

// ---------------------------------------------------------------- Word

var wordFormats = map[string]int{
	"pdf":  17, // wdFormatPDF
	"docx": 12, // wdFormatXMLDocument（不用 16「默认格式」，用户可能把默认格式改成了 doc）
	"doc":  0,  // wdFormatDocument
	"rtf":  6,  // wdFormatRTF
	"odt":  23, // wdFormatOpenDocumentText
	"html": 10, // wdFormatFilteredHTML
	"txt":  2,  // wdFormatText（配合 Encoding=65001 存成 UTF-8）
}

func wordSave(t *task, a *comApp, in, out, format string, o openOpts) error {
	code, ok := wordFormats[format]
	if !ok {
		return conv.Fail(a.name()+" 不能存成 "+upper(format), "")
	}
	docs, err := a.app.mustSub("Documents")
	if err != nil {
		return err
	}
	defer docs.release()
	if o.pdf {
		restore := allowPDFOpen(a.version)
		defer restore()
		t.report(-1, "正在用 "+a.name()+" 识别 PDF 内容（页数多时会比较慢）…")
	} else {
		t.report(-1, "正在用 "+a.name()+" 打开文件…")
	}
	var openFmt, enc any = missing, missing
	if o.codepage != 0 {
		openFmt, enc = 5, o.codepage // wdOpenFormatEncodedText
	}
	// Open(FileName, ConfirmConversions, ReadOnly, AddToRecentFiles, PasswordDocument, PasswordTemplate,
	//      Revert, WritePasswordDocument, WritePasswordTemplate, Format, Encoding, Visible, OpenAndRepair,
	//      DocumentDirection, NoEncodingDialog)
	doc, err := docs.mustSub("Open", in, false, true, false, bogusPW, bogusPW,
		false, missing, missing, openFmt, enc, false, missing, missing, true)
	if err != nil {
		return &openError{err}
	}
	defer func() {
		doc.call("Close", 0) // wdDoNotSaveChanges
		doc.release()
	}()
	if t.cancelled() {
		return conv.ErrCancelled
	}
	if o.text {
		wordTextStyle(doc)
	}
	if o.web {
		wordWebFix(doc)
	}
	if format == "txt" {
		wordTablesToText(doc)
	}
	// 上面只改了内存里的文档：标记成「已保存」，Word 的自动恢复就不会给它存备份
	doc.put("Saved", true)
	t.report(-1, "正在保存为 "+upper(format)+"…")
	if format == "pdf" {
		// ExportAsFixedFormat(OutputFileName, ExportFormat, OpenAfterExport, OptimizeFor, Range, From, To,
		//      Item, IncludeDocProps, KeepIRM, CreateBookmarks, DocStructureTags, BitmapMissingFonts, UseISO19005_1)
		err = doc.call("ExportAsFixedFormat", out, 17, false, 0, 0, 1, 1, 0, true, true, 1, true, true, false)
		if err != nil && !isDead(err) && !t.cancelled() {
			if err2 := wordSaveAs(doc, out, code, 0); err2 == nil {
				err = nil
			}
		}
		return err
	}
	enc2 := 0
	switch format {
	case "txt":
		enc2 = 65001
	case "html":
		enc2 = 65001
		if wo, err := doc.sub("WebOptions"); err == nil && wo.ok() {
			wo.put("Encoding", 65001) // msoEncodingUTF8：网页的编码由这里决定，SaveAs2 的 Encoding 参数管不到
			wo.release()
		}
	}
	return wordSaveAs(doc, out, code, enc2)
}

// wordTablesToText 存成纯文本前把表格变成「制表符分隔」的文字（不然每个单元格各占一行）。
// 只改内存里的文档，关闭时不保存。
func wordTablesToText(doc obj) {
	tables, err := doc.mustSub("Tables")
	if err != nil {
		return
	}
	defer tables.release()
	n, _ := tables.int("Count")
	for i := n; i >= 1; i-- {
		if tb, err := tables.mustSub("Item", i); err == nil {
			tb.call("ConvertToText", 1, true) // wdSeparateByTabs，嵌套表格一起转
			tb.release()
		}
	}
}

// wordSaveAs 调 SaveAs2（老版本 Word 和 WPS 没有 SaveAs2 时用 SaveAs）
func wordSaveAs(doc obj, out string, code, enc int) error {
	// SaveAs2(FileName, FileFormat, LockComments, Password, AddToRecentFiles, WritePassword,
	//         ReadOnlyRecommended, EmbedTrueTypeFonts, SaveNativePictureFormat, SaveFormsData,
	//         SaveAsAOCELetter, Encoding, InsertLineBreaks, AllowSubstitutions, LineEnding)
	args := []any{out, code, false, "", false, "", false, missing, missing, missing, missing}
	if enc != 0 {
		args = append(args, enc, false, true, 0) // wdCRLF
	}
	err := doc.call("SaveAs2", args...)
	if ce, ok := asCOMError(err); ok && ce.HR == hrUnknownName {
		err = doc.call("SaveAs", args...)
	}
	return err
}

// wordTextStyle 纯文本：换成微软雅黑、紧凑行距，免得出现奇怪的字体或者很大的段间距
func wordTextStyle(doc obj) {
	rng, err := doc.mustSub("Content")
	if err != nil {
		return
	}
	defer rng.release()
	if f, err := rng.mustSub("Font"); err == nil {
		f.put("Name", "微软雅黑")
		f.put("NameFarEast", "微软雅黑")
		f.put("Size", 10.5)
		f.release()
	}
	if pf, err := rng.mustSub("ParagraphFormat"); err == nil {
		pf.put("SpaceBefore", 0)
		pf.put("SpaceAfter", 0)
		pf.put("LineSpacingRule", 0)          // wdLineSpaceSingle
		pf.put("DisableLineHeightGrid", true) // 不对齐文档网格，否则微软雅黑会变成两倍行距
		pf.release()
	}
}

// wordWebFix 网页：把链接的图片存进文档、太宽的图片缩小到版心宽度，
// 切换到页面视图（不然生成的 docx 打开是「Web 版式」）
func wordWebFix(doc obj) {
	usable := 0.0
	if ps, err := doc.mustSub("PageSetup"); err == nil {
		pw, _ := ps.float("PageWidth")
		lm, _ := ps.float("LeftMargin")
		rm, _ := ps.float("RightMargin")
		usable = pw - lm - rm
		ps.release()
	}
	if shapes, err := doc.mustSub("InlineShapes"); err == nil {
		n, _ := shapes.int("Count")
		for i := 1; i <= n; i++ {
			s, err := shapes.mustSub("Item", i)
			if err != nil {
				continue
			}
			// 直接设置 Width 对网页导入的图片不起作用，按比例缩放可以
			w, _ := s.float("Width")
			if usable > 50 && w > usable {
				f := usable / w
				sw, _ := s.float("ScaleWidth")
				sh, _ := s.float("ScaleHeight")
				if sw <= 0 || sh <= 0 {
					sw, sh = 100, 100
				}
				s.put("ScaleWidth", sw*f)
				s.put("ScaleHeight", sh*f)
			}
			s.release()
		}
		shapes.release()
	}
	for _, coll := range []string{"InlineShapes", "Shapes"} {
		c, err := doc.mustSub(coll)
		if err != nil {
			continue
		}
		n, _ := c.int("Count")
		for i := n; i >= 1; i-- {
			s, err := c.mustSub("Item", i)
			if err != nil {
				continue
			}
			if lf, err := s.sub("LinkFormat"); err == nil && lf.ok() {
				lf.put("SavePictureWithDocument", true)
				lf.call("BreakLink")
				lf.release()
			}
			s.release()
		}
		c.release()
	}
	if w, err := doc.sub("ActiveWindow"); err == nil && w.ok() {
		if v, err := w.sub("View"); err == nil && v.ok() {
			v.put("Type", 3) // wdPrintView
			v.release()
		}
		w.release()
	}
}

// ---------------------------------------------------------------- Excel

var excelFormats = map[string]int{
	"xlsx": 51, // xlOpenXMLWorkbook
	"xls":  56, // xlExcel8
	"ods":  60, // xlOpenDocumentSpreadsheet
	"csv":  62, // xlCSVUTF8
	"html": 44, // xlHtml
}

func excelSave(t *task, a *comApp, in, out, format string, o openOpts) error {
	code, ok := excelFormats[format]
	if !ok && format != "pdf" {
		return conv.Fail(a.name()+" 不能存成 "+upper(format), "")
	}
	wbs, err := a.app.mustSub("Workbooks")
	if err != nil {
		return err
	}
	defer wbs.release()
	t.report(-1, "正在用 "+a.name()+" 打开文件…")
	// Open(Filename, UpdateLinks, ReadOnly, Format, Password, WriteResPassword, IgnoreReadOnlyRecommended,
	//      Origin, Delimiter, Editable, Notify, Converter, AddToMru)
	wb, err := wbs.mustSub("Open", in, 0, true, missing, bogusPW, missing, true, missing, missing, false, false, missing, false)
	if err != nil {
		return &openError{err}
	}
	defer func() {
		wb.call("Close", false)
		wb.release()
	}()
	if t.cancelled() {
		return conv.ErrCancelled
	}
	t.report(-1, "正在保存为 "+upper(format)+"…")
	if format == "pdf" {
		excelFitPages(a, wb, o.gridlines)
		// ExportAsFixedFormat(Type, Filename, Quality, IncludeDocProperties, IgnorePrintAreas)
		err = wb.call("ExportAsFixedFormat", 0, out, 0, true, false)
		if ce, ok := asCOMError(err); ok && !isDead(err) {
			if _, e := os.Stat(out); e != nil && (strings.Contains(ce.Desc, "打印") || strings.Contains(strings.ToLower(ce.Desc), "print")) {
				return conv.Fail("表格是空的，没有可以打印的内容", "")
			}
		}
		if err == nil && pdfHasNoPages(out) { // 空表格时 Excel 会生成一个 0 页的 PDF
			os.Remove(out)
			return conv.Fail("表格是空的，没有可以打印的内容", "")
		}
		return err
	}
	wb.put("CheckCompatibility", false) // 存成 xls 时不弹兼容性检查
	return wb.call("SaveAs", out, code)
}

// excelFitPages 比一页宽一点的工作表改成横向或者缩放到一页宽，免得一张表被拆成好几页。
// 用户自己设置过打印区域或缩放的不动。
// gridlines 为 true 时打印网格线（CSV 转来的表格没有边框，不打印网格线会很难看）。
func excelFitPages(a *comApp, wb obj, gridlines bool) {
	a.app.put("PrintCommunication", false) // 批量改页面设置时不和打印机反复通信
	defer a.app.put("PrintCommunication", true)
	sheets, err := wb.mustSub("Worksheets")
	if err != nil {
		return
	}
	defer sheets.release()
	n, _ := sheets.int("Count")
	const portrait, landscape = 490.0, 735.0 // A4 去掉默认页边距后的可打印宽度（磅）
	for i := 1; i <= n; i++ {
		ws, err := sheets.mustSub("Item", i)
		if err != nil {
			continue
		}
		func() {
			defer ws.release()
			if v, _ := ws.int("Visible"); v != -1 { // xlSheetVisible
				return
			}
			ps, err := ws.mustSub("PageSetup")
			if err != nil {
				return
			}
			defer ps.release()
			if gridlines {
				ps.put("PrintGridlines", true)
			}
			if area, _ := ps.str("PrintArea"); area != "" || ps.isBoolFalse("Zoom") {
				return
			}
			ur, err := ws.mustSub("UsedRange")
			if err != nil {
				return
			}
			w, _ := ur.float("Width")
			ur.release()
			switch {
			case w <= portrait:
			case w <= landscape:
				ps.put("Orientation", 2) // xlLandscape
			case w <= landscape*2.5:
				ps.put("Orientation", 2)
				ps.put("Zoom", false)
				ps.put("FitToPagesWide", 1)
				ps.put("FitToPagesTall", false)
			}
		}()
	}
}

// ---------------------------------------------------------------- PowerPoint

var pptFormats = map[string]int{
	"pdf":  32, // ppSaveAsPDF
	"pptx": 24, // ppSaveAsOpenXMLPresentation
	"ppt":  1,  // ppSaveAsPresentation
	"odp":  35, // ppSaveAsOpenDocumentPresentation
}

// pptOpen 打开演示文稿：只读、不显示窗口
func pptOpen(a *comApp, in string) (obj, error) {
	pres, err := a.app.mustSub("Presentations")
	if err != nil {
		return obj{}, err
	}
	defer pres.release()
	name := in
	if a.prog.vendor == vendorMS {
		name = in + "::" + bogusPW + "::" // 「文件名::打开密码::」：有密码时直接报错，不弹输入框
	}
	// Open(FileName, ReadOnly, Untitled, WithWindow)
	p, err := pres.mustSub("Open", name, -1, 0, 0)
	if err != nil {
		return obj{}, &openError{err}
	}
	return p, nil
}

func pptSave(t *task, a *comApp, in, out, format string) error {
	code, ok := pptFormats[format]
	if !ok {
		return conv.Fail(a.name()+" 不能存成 "+upper(format), "")
	}
	t.report(-1, "正在用 "+a.name()+" 打开文件…")
	p, err := pptOpen(a, in)
	if err != nil {
		return err
	}
	defer func() {
		p.call("Close")
		p.release()
	}()
	if t.cancelled() {
		return conv.ErrCancelled
	}
	if format == "pdf" {
		if n := slideCount(p); n == 0 {
			return conv.Fail("这个演示文稿里没有幻灯片", "")
		}
	}
	t.report(-1, "正在保存为 "+upper(format)+"…")
	return p.call("SaveAs", out, code)
}

func slideCount(p obj) int {
	s, err := p.mustSub("Slides")
	if err != nil {
		return -1
	}
	defer s.release()
	n, err := s.int("Count")
	if err != nil {
		return -1
	}
	return n
}

// video 用 PowerPoint 的「导出为视频」生成 1080p、30 帧的 MP4
func (e comEngine) video(t *task, in, out string, slideSec int) error {
	if e.kind != appPPT {
		return conv.Fail("只有 PowerPoint 能导出视频", "")
	}
	err := runCOM(t, appPPT, 4*time.Hour, func(a *comApp) error {
		t.report(-1, "正在用 "+a.name()+" 打开文件…")
		p, err := pptOpen(a, in)
		if err != nil {
			return err
		}
		defer func() {
			p.call("Close")
			p.release()
		}()
		n := slideCount(p)
		if n == 0 {
			return conv.Fail("这个演示文稿里没有幻灯片", "")
		}
		// CreateVideo(FileName, UseTimingsAndNarrations, DefaultSlideDuration, VertResolution, FramesPerSecond, Quality)
		if err := p.call("CreateVideo", out, false, slideSec, 1080, 30, 85); err != nil {
			return err
		}
		start := time.Now()
		for {
			st, err := p.int("CreateVideoStatus")
			if err != nil {
				return err
			}
			switch st {
			case 3: // ppMediaTaskStatusDone
				return nil
			case 4: // ppMediaTaskStatusFailed
				return conv.Fail(a.name()+" 生成视频失败", "")
			}
			if t.cancelled() {
				return conv.ErrCancelled
			}
			sec := int(time.Since(start).Seconds())
			t.report(-1, fmt.Sprintf("正在生成视频（%d 页），已用 %d 分 %02d 秒…", n, sec/60, sec%60))
			for i := 0; i < 5; i++ {
				pumpMessages()
				time.Sleep(100 * time.Millisecond)
			}
		}
	})
	return friendly(err, e.name())
}
