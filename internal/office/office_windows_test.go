//go:build windows

package office

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/sys/windows/registry"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 真正调用 Office 的测试。没装 Office 时跳过。

var officeExes = []string{"WINWORD.EXE", "EXCEL.EXE", "POWERPNT.EXE"}

// 测试开始前已经在运行的 Office 进程（用户自己的，不能动）
var preexisting = map[string]map[uint32]bool{}

func TestMain(m *testing.M) {
	for _, e := range officeExes {
		preexisting[e] = processes(e)
	}
	code := m.Run()
	Shutdown()
	if fixDir != "" {
		os.RemoveAll(fixDir)
	}
	os.Exit(code)
}

type fixtureSet struct {
	png, docx, pwDocx, wpwDocx, xlsx, pwXlsx, pptx, pwPptx string
	txt, md, html, mht, gbkCSV, bomCSV                     string
}

var (
	fixOnce sync.Once
	fixDir  string
	fix     fixtureSet
	fixErr  error
)

func needWord(t *testing.T) {
	t.Helper()
	if !hasCOM(appWord) || comProg(appWord).vendor != vendorMS {
		t.Skip("没有安装 Microsoft Word")
	}
}

func needOffice(t *testing.T) {
	t.Helper()
	for k := appWord; k < numApps; k++ {
		if !hasCOM(k) || comProg(k).vendor != vendorMS {
			t.Skip("没有安装完整的 Microsoft Office")
		}
	}
}

// fixtures 生成测试用的文件（文件夹名和文件名都带中文和空格）
func fixtures(t *testing.T) fixtureSet {
	t.Helper()
	needOffice(t)
	fixOnce.Do(func() {
		fixDir, fixErr = os.MkdirTemp("", "办公 转换 测试 *")
		if fixErr != nil {
			return
		}
		fixErr = makeFixtures(fixDir)
	})
	if fixErr != nil {
		t.Fatal("生成测试文件失败：", fixErr)
	}
	return fix
}

func makeFixtures(dir string) error {
	p := func(name string) string { return filepath.Join(dir, "素材 文件", name) }
	os.MkdirAll(p(""), 0o755)
	fix.png = p("示例 图片.png")
	os.WriteFile(fix.png, pngBytes(), 0o644)
	fix.docx, fix.pwDocx, fix.wpwDocx = p("测试 文档.docx"), p("带 密码.docx"), p("带修改密码.docx")
	fix.pptx, fix.pwPptx = p("测试 演示.pptx"), p("带 密码.pptx")
	tk := newTask(context.Background(), nil)
	defer tk.close()
	start := time.Now()
	err := runCOM(tk, appWord, 3*time.Minute, func(a *comApp) error {
		docs, err := a.app.mustSub("Documents")
		if err != nil {
			return err
		}
		defer docs.release()
		doc, err := docs.mustSub("Add")
		if err != nil {
			return err
		}
		defer func() { doc.call("Close", 0); doc.release() }()
		content, _ := doc.mustSub("Content")
		content.put("Text", "测试文档标题\r第一段：这是一段中文正文，用来测试格式转换。English text 123.\r第二段：下面是一个表格。\r")
		content.release()
		paras, _ := doc.mustSub("Paragraphs")
		p1, _ := paras.mustSub("Item", 1)
		p1.put("Style", -2) // wdStyleHeading1
		p1.release()
		last, _ := paras.mustSub("Last")
		rng, _ := last.mustSub("Range")
		last.release()
		paras.release()
		tables, _ := doc.mustSub("Tables")
		tbl, err := tables.mustSub("Add", rng, 4, 3)
		rng.release()
		tables.release()
		if err != nil {
			return err
		}
		if b, err := tbl.mustSub("Borders"); err == nil {
			b.put("Enable", true)
			b.release()
		}
		data := [][]string{{"姓名", "部门", "工号"}, {"张三", "研发部", "00123"}, {"李四", "市场部", "00456"}, {"王五", "财务部", "789"}}
		for r, row := range data {
			for c, s := range row {
				cell, err := tbl.mustSub("Cell", r+1, c+1)
				if err != nil {
					return err
				}
				cr, _ := cell.mustSub("Range")
				cr.put("Text", s)
				cr.release()
				cell.release()
			}
		}
		tbl.release()
		content, _ = doc.mustSub("Content")
		content.call("InsertParagraphAfter")
		content.call("InsertAfter", "图片如下：")
		content.release()
		paras, _ = doc.mustSub("Paragraphs")
		last, _ = paras.mustSub("Last")
		rng, _ = last.mustSub("Range")
		shapes, _ := doc.mustSub("InlineShapes")
		if err := shapes.call("AddPicture", fix.png, false, true, rng); err != nil {
			return err
		}
		shapes.release()
		rng.release()
		last.release()
		paras.release()
		if err := doc.call("SaveAs2", fix.docx, 12); err != nil {
			return err
		}
		fix.mht = p("网页 存档.mht")
		if err := doc.call("SaveAs2", fix.mht, 9); err != nil { // wdFormatWebArchive
			return err
		}
		if err := doc.call("SaveAs2", fix.pwDocx, 12, false, "pw123"); err != nil {
			return err
		}
		return doc.call("SaveAs2", fix.wpwDocx, 12, false, "", false, "wpw123")
	})
	if err != nil {
		return fmt.Errorf("Word：%w", err)
	}
	fmt.Printf("生成 Word 素材用了 %.1f 秒（含启动 Word）\n", time.Since(start).Seconds())
	err = runCOM(tk, appPPT, 3*time.Minute, func(a *comApp) error {
		ps, err := a.app.mustSub("Presentations")
		if err != nil {
			return err
		}
		defer ps.release()
		pres, err := ps.mustSub("Add", 0) // msoFalse：不显示窗口
		if err != nil {
			return err
		}
		defer func() { pres.call("Close"); pres.release() }()
		slides, _ := pres.mustSub("Slides")
		defer slides.release()
		for i := 1; i <= 3; i++ {
			s, err := slides.mustSub("Add", i, 2) // ppLayoutText
			if err != nil {
				return err
			}
			shapes, _ := s.mustSub("Shapes")
			for k, text := range []string{fmt.Sprintf("第 %d 页：标题", i), "这是幻灯片正文\r第二条要点"} {
				sh, err := shapes.mustSub("Item", k+1)
				if err != nil {
					return err
				}
				tf, _ := sh.mustSub("TextFrame")
				tr, _ := tf.mustSub("TextRange")
				tr.put("Text", text)
				tr.release()
				tf.release()
				sh.release()
			}
			shapes.release()
			s.release()
		}
		if err := pres.call("SaveAs", fix.pptx, 24); err != nil {
			return err
		}
		pres.put("Password", "pw123")
		return pres.call("SaveAs", fix.pwPptx, 24)
	})
	if err != nil {
		return fmt.Errorf("PowerPoint：%w", err)
	}

	// 表格用 excelize 生成
	fix.xlsx = p("销售 报表.xlsx")
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "销售")
	rows := [][]any{
		{"编号", "产品", "数量", "单价", "日期", "备注"},
		{"00123", "苹果", 10, 3.5, time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC), "第一批"},
		{"00456", "香蕉", 200, 1.25, time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), "含,逗号"},
		{"789", "橙子", 0, 12345678.9, time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC), ""},
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		f.SetSheetRow("销售", cell, &r)
	}
	ds, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	f.SetCellStyle("销售", "E2", "E4", ds)
	f.NewSheet("说明")
	f.SetCellValue("说明", "A1", "这是第二个工作表")
	if err := f.SaveAs(fix.xlsx); err != nil {
		return err
	}
	fix.pwXlsx = p("带 密码.xlsx")
	if err := f.SaveAs(fix.pwXlsx, excelize.Options{Password: "pw123"}); err != nil {
		return err
	}
	f.Close()

	fix.txt = p("GBK 文本.txt")
	os.WriteFile(fix.txt, gbk("纯文本标题\r\n\r\n这是用 GBK 编码保存的中文文本。第二句话。\r\n\tTab 开头的一行\r\nEnglish line 456\r\n"), 0o644)
	os.MkdirAll(p("图片 目录"), 0o755)
	os.WriteFile(p(filepath.Join("图片 目录", "图 一.png")), pngBytes(), 0o644)
	fix.md = p("说明 文档.md")
	os.WriteFile(fix.md, []byte("# Markdown 标题\n\n中文段落，**加粗**和`代码`。\n\n| 名称 | 数量 |\n|---|---:|\n| 苹果 | 10 |\n| 香蕉 | 20 |\n\n```python\nprint(\"你好\")\n```\n\n![图](图片%20目录/图%20一.png)\n"), 0o644)
	fix.html = p("网页 文件.html")
	os.WriteFile(fix.html, []byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>网页标题</title><style>td{border:1px solid #999;padding:4px}</style></head>
<body><h1>网页里的标题</h1><p>这是网页正文，有<b>粗体</b>。</p><table><tr><td>甲</td><td>乙</td></tr><tr><td>100</td><td>200</td></tr></table>
<p><img src="图片 目录/图 一.png" alt="图"></p></body></html>`), 0o644)
	fix.gbkCSV = p("GBK 数据.csv")
	os.WriteFile(fix.gbkCSV, gbk(csvText), 0o644)
	fix.bomCSV = p("BOM 数据.csv")
	os.WriteFile(fix.bomCSV, append(append([]byte(nil), bomUTF8...), csvText...), 0o644)
	return nil
}

// ---------------------------------------------------------------- 检查结果

var rePage = regexp.MustCompile(`/Type\s*/Page[^s]`)
var reCount = regexp.MustCompile(`/Count\s+(\d+)`)

func checkPDF(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "%PDF-") {
		t.Fatalf("%s 不是 PDF", filepath.Base(p))
	}
	n := len(rePage.FindAll(b, -1))
	if n == 0 {
		for _, m := range reCount.FindAllSubmatch(b, -1) {
			if c, _ := strconv.Atoi(string(m[1])); c > n {
				n = c
			}
		}
	}
	if n == 0 && !strings.Contains(string(b), "/ObjStm") {
		t.Errorf("%s 里没有页面", filepath.Base(p))
	}
	return n
}

func zipPart(t *testing.T, p, part string) string {
	t.Helper()
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatalf("%s 不是有效的 zip：%v", filepath.Base(p), err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == part || strings.HasSuffix(part, "/") && strings.HasPrefix(f.Name, part) {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			return string(b)
		}
	}
	t.Errorf("%s 里没有 %s", filepath.Base(p), part)
	return ""
}

var reTag = regexp.MustCompile(`<[^>]+>`)

func xmlText(s string) string { return reTag.ReplaceAllString(s, "") }

func checkFile(t *testing.T, p string, min int64) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("没有输出文件：%v", err)
	}
	if fi.Size() < min {
		t.Errorf("%s 太小：%d 字节", filepath.Base(p), fi.Size())
	}
}

func readText(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := decodeText(b)
	return s
}

// convOne 转换一个文件，要求恰好一个输出
func convOne(t *testing.T, in, to string, opt conv.Options) string {
	t.Helper()
	outs, err := runJob(t, Convert(to), in, opt)
	if err != nil {
		t.Fatalf("%s → %s 失败：%v", filepath.Base(in), to, err)
	}
	if len(outs) != 1 {
		t.Fatalf("应该有一个输出：%v", outs)
	}
	if conv.Ext(outs[0]) != "."+to {
		t.Errorf("扩展名不对：%s", outs[0])
	}
	return outs[0]
}

// verify 根据格式检查输出内容
func verify(t *testing.T, p string, wants ...string) {
	t.Helper()
	checkFile(t, p, 10)
	var text string
	switch conv.Ext(p) {
	case ".pdf":
		checkPDF(t, p)
		return
	case ".docx":
		zipPart(t, p, "[Content_Types].xml")
		text = xmlText(zipPart(t, p, "word/document.xml"))
	case ".xlsx":
		zipPart(t, p, "xl/workbook.xml")
		f, err := excelize.OpenFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range f.GetSheetList() {
			rows, _ := f.GetRows(s)
			for _, r := range rows {
				text += strings.Join(r, "|") + "\n"
			}
		}
		f.Close()
	case ".pptx":
		zipPart(t, p, "ppt/presentation.xml")
		text = xmlText(zipPart(t, p, "ppt/slides/slide1.xml"))
	case ".odt", ".ods", ".odp":
		if !strings.Contains(zipPart(t, p, "mimetype"), "opendocument") {
			t.Error("mimetype 不对")
		}
		text = xmlText(zipPart(t, p, "content.xml"))
	case ".doc", ".xls", ".ppt":
		b, _ := os.ReadFile(p)
		if !strings.HasPrefix(string(b), "\xD0\xCF\x11\xE0") {
			t.Errorf("%s 不是老格式的 Office 文件", filepath.Base(p))
		}
		return
	case ".rtf":
		if !strings.HasPrefix(readText(t, p), `{\rtf`) {
			t.Error("不是 RTF")
		}
		return
	case ".mp4":
		b := make([]byte, 12)
		f, _ := os.Open(p)
		io.ReadFull(f, b)
		f.Close()
		if string(b[4:8]) != "ftyp" {
			t.Errorf("不是 MP4：%q", b)
		}
		return
	case ".txt", ".csv":
		b, _ := os.ReadFile(p)
		if !strings.HasPrefix(string(b), "\xEF\xBB\xBF") {
			t.Error("应该是带 BOM 的 UTF-8")
		}
		text = string(b)
	case ".html":
		text = readText(t, p)
		if !strings.Contains(strings.ToLower(text), "utf-8") {
			t.Error("网页应该声明 UTF-8")
		}
	}
	if strings.HasPrefix(conv.Ext(p), ".od") { // ODF 里的空格写成 <text:s/>
		text = strings.ReplaceAll(text, " ", "")
	}
	for _, w := range wants {
		if strings.HasPrefix(conv.Ext(p), ".od") {
			w = strings.ReplaceAll(w, " ", "")
		}
		if !strings.Contains(text, w) {
			t.Errorf("%s 里没有「%s」", filepath.Base(p), w)
		}
	}
}

// ---------------------------------------------------------------- 测试

func TestWordConversions(t *testing.T) {
	fx := fixtures(t)
	start := time.Now()
	pdf := convOne(t, fx.docx, "pdf", nil)
	t.Logf("第一次 Word 转 PDF：%.1f 秒", time.Since(start).Seconds())
	if n := checkPDF(t, pdf); n < 1 {
		t.Errorf("页数：%d", n)
	}
	start = time.Now()
	convOne(t, fx.docx, "pdf", nil)
	t.Logf("第二次 Word 转 PDF（复用 Word）：%.1f 秒", time.Since(start).Seconds())

	verify(t, convOne(t, fx.docx, "docx", nil), "测试文档标题", "张三", "00123")
	doc := convOne(t, fx.docx, "doc", nil)
	verify(t, doc)
	verify(t, convOne(t, fx.docx, "rtf", nil))
	verify(t, convOne(t, fx.docx, "odt", nil), "测试文档标题", "研发部")
	verify(t, convOne(t, fx.docx, "txt", nil), "测试文档标题", "第一段：这是一段中文正文", "张三")
	h := convOne(t, fx.docx, "html", nil)
	verify(t, h, "测试文档标题", "<table", "data:image/")
	if ents, _ := os.ReadDir(filepath.Dir(h)); len(ents) > 0 {
		for _, e := range ents {
			if e.IsDir() {
				t.Errorf("网页不该带附属文件夹：%s", e.Name())
			}
		}
	}
	// 老格式 doc 再转回来
	verify(t, convOne(t, doc, "docx", nil), "测试文档标题", "李四")
	verify(t, convOne(t, doc, "pdf", nil))
	// WPS 扩展名（内容其实是 docx）
	wps := filepath.Join(filepath.Dir(fx.docx), "金山 文档.wps")
	copyFile(fx.docx, wps)
	verify(t, convOne(t, wps, "pdf", nil))
	// 只有修改密码的文件可以只读打开
	verify(t, convOne(t, fx.wpwDocx, "pdf", nil))
	// 输出的 docx 能被 Word 正常打开
	reopenWord(t, convOne(t, fx.docx, "docx", nil))
}

// reopenWord 用 Word 打开输出文件，确认它没坏
func reopenWord(t *testing.T, p string) {
	t.Helper()
	tk := newTask(context.Background(), nil)
	defer tk.close()
	var n int
	err := runCOM(tk, appWord, time.Minute, func(a *comApp) error {
		docs, _ := a.app.mustSub("Documents")
		defer docs.release()
		doc, err := docs.mustSub("Open", p, false, true, false, bogusPW, bogusPW, false, missing, missing, missing, missing, false)
		if err != nil {
			return err
		}
		defer func() { doc.call("Close", 0); doc.release() }()
		ps, _ := doc.mustSub("Paragraphs")
		defer ps.release()
		n, err = ps.int("Count")
		return err
	})
	if err != nil || n < 3 {
		t.Errorf("Word 重新打开 %s：%d 段，%v", filepath.Base(p), n, err)
	}
}

func TestExcelConversions(t *testing.T) {
	fx := fixtures(t)
	start := time.Now()
	verify(t, convOne(t, fx.xlsx, "pdf", nil))
	t.Logf("第一次 Excel 转 PDF：%.1f 秒", time.Since(start).Seconds())
	x := convOne(t, fx.xlsx, "xlsx", nil)
	verify(t, x, "00123", "苹果")
	xls := convOne(t, fx.xlsx, "xls", nil)
	verify(t, xls)
	verify(t, convOne(t, fx.xlsx, "ods", nil), "00123", "香蕉")
	verify(t, convOne(t, fx.xlsx, "html", nil), "苹果", "<h2>说明</h2>")

	outs, err := runJob(t, Convert("csv"), fx.xlsx, nil)
	if err != nil || len(outs) != 2 {
		t.Fatalf("xlsx → csv：%v %v", outs, err)
	}
	verify(t, outs[0], "00123,苹果,10,3.5,2024/1/5,第一批", "\"含,逗号\"")
	verify(t, outs[1], "这是第二个工作表")

	// 老格式 xls → csv / html / pdf（要经过 Excel）
	outs, err = runJob(t, Convert("csv"), xls, nil)
	if err != nil || len(outs) != 2 {
		t.Fatalf("xls → csv：%v %v", outs, err)
	}
	verify(t, outs[0], "00123,苹果", "2024/1/5")
	verify(t, convOne(t, xls, "html", nil), "香蕉")
	verify(t, convOne(t, xls, "xlsx", nil), "00123")
	// WPS 表格扩展名
	et := filepath.Join(filepath.Dir(fx.xlsx), "金山 表格.et")
	copyFile(fx.xlsx, et)
	outs, err = runJob(t, Convert("csv"), et, nil)
	if err != nil || len(outs) != 2 {
		t.Fatalf("et → csv：%v %v", outs, err)
	}
	reopenExcel(t, x)
}

func reopenExcel(t *testing.T, p string) {
	t.Helper()
	tk := newTask(context.Background(), nil)
	defer tk.close()
	var v string
	err := runCOM(tk, appExcel, time.Minute, func(a *comApp) error {
		wbs, _ := a.app.mustSub("Workbooks")
		defer wbs.release()
		wb, err := wbs.mustSub("Open", p, 0, true)
		if err != nil {
			return err
		}
		defer func() { wb.call("Close", false); wb.release() }()
		ws, _ := wb.mustSub("Worksheets", 1)
		defer ws.release()
		r, _ := ws.mustSub("Range", "B2")
		defer r.release()
		v, err = r.str("Text")
		return err
	})
	if err != nil || v != "苹果" {
		t.Errorf("Excel 重新打开 %s：%q %v", filepath.Base(p), v, err)
	}
}

func TestCSVWithOffice(t *testing.T) {
	fx := fixtures(t)
	for _, in := range []string{fx.gbkCSV, fx.bomCSV} {
		x := convOne(t, in, "xlsx", nil)
		checkCSVWorkbook(t, x)
		verify(t, convOne(t, in, "xls", nil))
		verify(t, convOne(t, in, "ods", nil), "00123", "张三")
		verify(t, convOne(t, in, "pdf", nil))
		verify(t, convOne(t, in, "html", nil), "张三", "00123", "110101199003071234")
		c := convOne(t, in, "csv", nil)
		verify(t, c, "00123,张三,28,1234.5,2024-01-05,13800138000,110101199003071234")
	}
}

func TestPowerPointConversions(t *testing.T) {
	fx := fixtures(t)
	start := time.Now()
	pdf := convOne(t, fx.pptx, "pdf", nil)
	t.Logf("PowerPoint 转 PDF：%.1f 秒", time.Since(start).Seconds())
	if n := checkPDF(t, pdf); n != 3 && n != 0 {
		t.Errorf("应该有 3 页：%d", n)
	}
	verify(t, convOne(t, fx.pptx, "pptx", nil), "第 1 页：标题")
	ppt := convOne(t, fx.pptx, "ppt", nil)
	verify(t, ppt)
	verify(t, convOne(t, fx.pptx, "odp", nil), "第 1 页：标题")
	verify(t, convOne(t, ppt, "pptx", nil), "第 1 页：标题")
	start = time.Now()
	mp4 := convOne(t, fx.pptx, "mp4", conv.Options{conv.OptSlideSec: "1"})
	t.Logf("PowerPoint 转 MP4（3 页 × 1 秒）：%.1f 秒", time.Since(start).Seconds())
	verify(t, mp4)
}

func TestTextMarkdownHTML(t *testing.T) {
	fx := fixtures(t)
	verify(t, convOne(t, fx.txt, "pdf", nil))
	d := convOne(t, fx.txt, "docx", nil)
	verify(t, d, "纯文本标题", "这是用 GBK 编码保存的中文文本", "English line 456")
	if doc := zipPart(t, d, "word/document.xml"); !strings.Contains(doc, "微软雅黑") && !strings.Contains(doc, "Microsoft YaHei") {
		t.Error("纯文本应该换成微软雅黑")
	}
	verify(t, convOne(t, fx.txt, "html", nil), "这是用 GBK 编码保存的中文文本")

	verify(t, convOne(t, fx.md, "html", nil), "<table>", "data:image/png")
	verify(t, convOne(t, fx.md, "pdf", nil))
	md := convOne(t, fx.md, "docx", nil)
	verify(t, md, "Markdown 标题", "苹果", "print")
	zipPart(t, md, "word/media/")

	verify(t, convOne(t, fx.html, "pdf", nil))
	hd := convOne(t, fx.html, "docx", nil)
	verify(t, hd, "网页里的标题", "甲", "200")
	zipPart(t, hd, "word/media/") // 网页里的图片要存进 docx
	verify(t, convOne(t, fx.html, "txt", nil), "网页里的标题", "这是网页正文")
	verify(t, convOne(t, fx.mht, "pdf", nil))
	verify(t, convOne(t, fx.mht, "docx", nil), "测试文档标题", "张三")
	verify(t, convOne(t, fx.mht, "txt", nil), "测试文档标题")

	// 没有 Edge 时用 Word 生成 PDF
	forceNoEdge = true
	defer func() { forceNoEdge = false }()
	verify(t, convOne(t, fx.md, "pdf", nil))
	verify(t, convOne(t, fx.html, "pdf", nil))
	verify(t, convOne(t, fx.txt, "pdf", nil))
}

func TestPDFToWordExcel(t *testing.T) {
	fx := fixtures(t)
	pdf := filepath.Join(filepath.Dir(fx.docx), "带 表格.pdf")
	if err := ToPDF(context.Background(), fx.docx, pdf); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	outs, err := runJob(t, PDFToWord(), pdf, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PDF 转 Word：%.1f 秒", time.Since(start).Seconds())
	verify(t, outs[0], "测试文档标题", "张三", "研发部")

	outs, err = runJob(t, PDFToExcel(), pdf, nil)
	if err != nil {
		t.Fatal(err)
	}
	verify(t, outs[0], "姓名|部门|工号", "张三|研发部|00123", "王五|财务部|789")

	// Convert 也能处理 PDF
	verify(t, convOne(t, pdf, "docx", nil), "李四")

	// 没有表格的 PDF：文字一段一行
	txtPDF := filepath.Join(filepath.Dir(fx.docx), "纯文字.pdf")
	if err := ToPDF(context.Background(), fx.txt, txtPDF); err != nil {
		t.Fatal(err)
	}
	outs, err = runJob(t, PDFToExcel(), txtPDF, nil)
	if err != nil {
		t.Fatal(err)
	}
	verify(t, outs[0], "纯文本标题")
}

func TestToPDF(t *testing.T) {
	fx := fixtures(t)
	dir := filepath.Join(fixDir, "导出 PDF")
	os.MkdirAll(dir, 0o755)
	for i, in := range []string{fx.docx, fx.xlsx, fx.pptx, fx.md, fx.html, fx.txt, fx.gbkCSV} {
		out := filepath.Join(dir, fmt.Sprintf("结果 %d.pdf", i))
		if err := ToPDF(context.Background(), in, out); err != nil {
			t.Errorf("%s：%v", filepath.Base(in), err)
			continue
		}
		checkPDF(t, out)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 7 {
		t.Errorf("应该只有 7 个 PDF，实际 %d 个", len(ents))
	}
}

// 从网上下载的文件带「来自网络」标记（Zone.Identifier），不能因为受保护的视图卡住
func TestMarkOfTheWeb(t *testing.T) {
	fx := fixtures(t)
	dir := filepath.Join(fixDir, "下载 的 文件")
	os.MkdirAll(dir, 0o755)
	motw := func(src, name string) string {
		p := filepath.Join(dir, name)
		copyFile(src, p)
		if err := os.WriteFile(p+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\nHostUrl=https://example.com/x\r\n"), 0o644); err != nil {
			t.Fatal("写不了 Zone.Identifier：", err)
		}
		if _, err := os.Stat(p + ":Zone.Identifier"); err != nil {
			t.Fatal(err)
		}
		return p
	}
	verify(t, convOne(t, motw(fx.docx, "下载的 文档.docx"), "pdf", nil))
	verify(t, convOne(t, motw(fx.xlsx, "下载的 表格.xlsx"), "pdf", nil))
	verify(t, convOne(t, motw(fx.pptx, "下载的 演示.pptx"), "pdf", nil))
	// 网页是在原位置打开的（不复制），标记也在
	os.MkdirAll(filepath.Join(dir, "图片 目录"), 0o755)
	copyFile(filepath.Join(filepath.Dir(fx.html), "图片 目录", "图 一.png"), filepath.Join(dir, "图片 目录", "图 一.png"))
	verify(t, convOne(t, motw(fx.html, "下载的 网页.html"), "docx", nil), "网页里的标题")
	forceNoEdge = true
	verify(t, convOne(t, motw(fx.html, "下载的 网页2.html"), "pdf", nil))
	forceNoEdge = false
}

func TestPasswordFiles(t *testing.T) {
	fx := fixtures(t)
	for _, in := range []string{fx.pwDocx, fx.pwXlsx, fx.pwPptx} {
		start := time.Now()
		_, err := runJob(t, Convert("pdf"), in, nil)
		var ue *conv.UserError
		if !errors.As(err, &ue) || ue.Msg != "这个文件有打开密码，没法转换" {
			t.Errorf("%s：应该提示有密码，实际 %v", filepath.Base(in), err)
		}
		if d := time.Since(start); d > 30*time.Second {
			t.Errorf("%s：等了 %.0f 秒才报错", filepath.Base(in), d.Seconds())
		}
	}
	// 有密码的 xlsx 转 csv（走 Excel）也一样
	_, err := runJob(t, Convert("csv"), fx.pwXlsx, nil)
	if err == nil || !strings.Contains(err.Error(), "密码") {
		t.Errorf("有密码的 xlsx 转 csv：%v", err)
	}
	// 之后正常文件照常能转
	verify(t, convOne(t, fx.docx, "pdf", nil))
}

// 只有「修改密码」的文件：只读打开就行，不能卡在输入密码的对话框上
func TestWritePasswordFiles(t *testing.T) {
	fx := fixtures(t)
	dir := filepath.Join(fixDir, "修改 密码")
	os.MkdirAll(dir, 0o755)
	xl, pp := filepath.Join(dir, "表格 修改密码.xlsx"), filepath.Join(dir, "演示 修改密码.pptx")
	tk := newTask(context.Background(), nil)
	defer tk.close()
	err := runCOM(tk, appExcel, time.Minute, func(a *comApp) error {
		wbs, _ := a.app.mustSub("Workbooks")
		defer wbs.release()
		wb, err := wbs.mustSub("Open", fx.xlsx)
		if err != nil {
			return err
		}
		defer func() { wb.call("Close", false); wb.release() }()
		return wb.call("SaveAs", xl, 51, "", "wpw123") // WriteResPassword
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runCOM(tk, appPPT, time.Minute, func(a *comApp) error {
		p, err := pptOpen(a, fx.pptx)
		if err != nil {
			return err
		}
		defer func() { p.call("Close"); p.release() }()
		p.put("WritePassword", "wpw123")
		return p.call("SaveAs", pp, 24)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{xl, pp} {
		start := time.Now()
		verify(t, convOne(t, in, "pdf", nil))
		if d := time.Since(start); d > 20*time.Second {
			t.Errorf("%s 用了 %.0f 秒", filepath.Base(in), d.Seconds())
		}
	}
}

func TestSameFormatAndConcurrent(t *testing.T) {
	fx := fixtures(t)
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, job := range []struct{ in, to string }{{fx.docx, "pdf"}, {fx.xlsx, "pdf"}, {fx.pptx, "pdf"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = runJob(t, Convert(job.to), job.in, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("同时转换第 %d 个：%v", i, err)
		}
	}
}

func TestCancel(t *testing.T) {
	fx := fixtures(t)
	// 1. 正在导出视频时取消
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(4*time.Second, cancel)
	start := time.Now()
	outs, err := runJobCtx(ctx, t, Convert("mp4"), fx.pptx, conv.Options{conv.OptSlideSec: "120"})
	if !errors.Is(err, conv.ErrCancelled) {
		t.Errorf("应该返回已取消：%v %v", outs, err)
	}
	if d := time.Since(start); d > 25*time.Second {
		t.Errorf("取消后等了 %.0f 秒", d.Seconds())
	}
	ents, _ := os.ReadDir(filepath.Join(filepath.Dir(fx.pptx), "输出 结果"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "测试 演示 (") && strings.HasSuffix(e.Name(), ".mp4") {
			t.Errorf("取消后留下了 %s", e.Name())
		}
	}
	// 之后还能正常用
	verify(t, convOne(t, fx.pptx, "pdf", nil))

	// 2. COM 调用卡住时取消：结束我们自己的 Word 进程，下一个任务自动换新的 Word
	tk := newTask(context.Background(), nil)
	ctx2, cancel2 := context.WithCancel(context.Background())
	tk.ctx = ctx2
	var pid uint32
	time.AfterFunc(time.Second, cancel2)
	start = time.Now()
	err = runCOM(tk, appWord, time.Minute, func(a *comApp) error {
		pid = a.pid
		time.Sleep(cancelGrace + 3*time.Second) // 模拟卡住
		_, err := a.app.str("Name")
		return err
	})
	if !errors.Is(err, conv.ErrCancelled) {
		t.Errorf("应该返回已取消：%v", err)
	}
	t.Logf("卡住的 Word（进程 %d）取消用了 %.1f 秒", pid, time.Since(start).Seconds())
	if pid == 0 || processAlive(pid) {
		t.Errorf("卡住的 Word 进程 %d 应该被结束", pid)
	}
	if preexisting["WINWORD.EXE"][pid] {
		t.Fatal("结束了用户自己的 Word！")
	}
	start = time.Now()
	verify(t, convOne(t, fx.docx, "pdf", nil))
	t.Logf("Word 被结束后重新启动并转换：%.1f 秒", time.Since(start).Seconds())
}

// 用户把我们的 Word 关掉了：下一个任务自动换一个新的
func TestAppKilledBetweenJobs(t *testing.T) {
	fx := fixtures(t)
	tk := newTask(context.Background(), nil)
	defer tk.close()
	var pid uint32
	runCOM(tk, appWord, time.Minute, func(a *comApp) error { pid = a.pid; return nil })
	if pid == 0 || preexisting["WINWORD.EXE"][pid] {
		t.Fatalf("Word 进程号不对：%d", pid)
	}
	worker().kill(pid) // 结束并清理崩溃记录（免得影响后面的测试）
	verify(t, convOne(t, fx.docx, "pdf", nil))
}

// 强制结束我们的 Office 后，不能留下「上次出现严重错误」之类的崩溃记录
func TestKillLeavesNoResiliency(t *testing.T) {
	fx := fixtures(t)
	ins := map[appKind]string{appWord: fx.docx, appExcel: fx.xlsx, appPPT: fx.pptx}
	for k := appWord; k < numApps; k++ {
		verify(t, convOne(t, ins[k], "pdf", nil))
		tk := newTask(context.Background(), nil)
		var pid uint32
		runCOM(tk, k, time.Minute, func(a *comApp) error { pid = a.pid; return nil })
		tk.close()
		w := worker()
		w.mu.Lock()
		op, ok := w.owned[pid]
		w.mu.Unlock()
		if !ok {
			t.Fatalf("%s 不是我们启动的", regAppName[k])
		}
		killPID(pid) // 先不清理，看看会留下什么
		var leftover []string
		for item := range snapshotResiliency(k) {
			if !op.resil[item] {
				leftover = append(leftover, item)
			}
		}
		t.Logf("强制结束 %s 后留下的记录：%v", regAppName[k], leftover)
		restoreResiliency(k, op.resil)
		for item := range snapshotResiliency(k) {
			if !op.resil[item] {
				t.Errorf("%s 的崩溃记录没清理掉：%s", regAppName[k], item)
			}
		}
		// 之后照常能转换（不会卡在「安全模式」提示上）
		verify(t, convOne(t, ins[k], "pdf", nil))
	}
}

func TestExcelEdgeCases(t *testing.T) {
	needOffice(t)
	dir := filepath.Join(chineseDir(t), "表格 特殊情况")
	os.MkdirAll(dir, 0o755)
	// 空表格转 PDF：友好提示
	f := excelize.NewFile()
	empty := filepath.Join(dir, "空 表格.xlsx")
	f.SaveAs(empty)
	f.Close()
	// 空表格：Excel 会生成 0 页的 PDF，要给出友好提示
	_, err := runJob(t, Convert("pdf"), empty, nil)
	var ue *conv.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "空") {
		t.Errorf("空表格：%v", err)
	}
	// 比较宽的表：横向、缩放到一页宽
	f = excelize.NewFile()
	for r := 1; r <= 30; r++ {
		for c := 1; c <= 16; c++ {
			cell, _ := excelize.CoordinatesToCellName(c, r)
			f.SetCellValue("Sheet1", cell, fmt.Sprintf("第%d行第%d列", r, c))
		}
	}
	f.SetColWidth("Sheet1", "A", "P", 12)
	wide := filepath.Join(dir, "很宽的 表格.xlsx")
	f.SaveAs(wide)
	f.Close()
	pdf := convOne(t, wide, "pdf", nil)
	if n := checkPDF(t, pdf); n != 1 {
		t.Errorf("16 列的表应该缩放到 1 页，实际 %d 页", n)
	}
	// 网页伪装成的 xls（很多网站导出的「Excel」其实是网页）
	fake := filepath.Join(dir, "网站导出.xls")
	os.WriteFile(fake, []byte(`<html><head><meta charset="utf-8"></head><body><table><tr><td>名称</td><td>数量</td></tr><tr><td>苹果</td><td>5</td></tr></table></body></html>`), 0o644)
	verify(t, convOne(t, fake, "xlsx", nil), "苹果")
}

// 提示框检测：用一个弹出消息框的 PowerShell 进程测试 dialogsOf
func TestDialogDetection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
		"Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.MessageBox]::Show('是否仍要打开它？测试提示框', 'Microsoft Word', 'YesNo')")
	if err := cmd.Start(); err != nil {
		t.Skip("没法启动 PowerShell：", err)
	}
	defer cmd.Process.Kill()
	pid := uint32(cmd.Process.Pid)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if d := dialogsOf(pid); len(d) > 0 {
			if !strings.Contains(d[0], "测试提示框") {
				t.Errorf("提示框文字：%q", d)
			}
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Error("没有检测到提示框")
}

func TestDetect(t *testing.T) {
	start := time.Now()
	info := Detect()
	t.Logf("Detect 用了 %v：%+v", time.Since(start), info)
	for k := appWord; k < numApps; k++ {
		t.Logf("%s：%+v", comProg(k).appName(k), comProg(k))
	}
	if hasCOM(appWord) && info.Word == "" || info.Word != "" && !HasWord() {
		t.Error("HasWord 和 Info.Word 不一致")
	}
	if p := comProg(appWord); p.progID != "" && !strings.EqualFold(filepath.Ext(p.exe), ".exe") {
		t.Errorf("进程名不对：%q", p.exe)
	}
}

// 用户自己开着 PowerPoint 时：转换借用它的实例，但不能把它关掉，设置要还原
func TestUserPowerPointOpen(t *testing.T) {
	fx := fixtures(t)
	if len(preexisting["POWERPNT.EXE"]) > 0 {
		t.Skip("用户已经开着 PowerPoint")
	}
	Shutdown() // 先退出我们自己的 PowerPoint
	for i := 0; i < 50 && len(processes("POWERPNT.EXE")) > 0; i++ {
		time.Sleep(200 * time.Millisecond)
	}
	if len(processes("POWERPNT.EXE")) > 0 {
		t.Skip("还有 PowerPoint 在运行")
	}
	cmd := exec.Command(comProg(appPPT).path)
	if err := cmd.Start(); err != nil {
		t.Skip("没法启动 PowerPoint：", err)
	}
	pid := uint32(cmd.Process.Pid)
	defer func() {
		// 像用户一样关闭窗口（不强制结束，免得下次启动提示「上次出现严重错误」）
		for _, h := range enumWindows(0, pid) {
			if windowClass(h) == "PPTFrameClass" {
				procPostMessageW.Call(h, 0x0010, 0, 0) // WM_CLOSE
			}
		}
		if !waitExit(pid, 15*time.Second) {
			killPID(pid)
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for !userVisible(pid) {
		if time.Now().After(deadline) {
			t.Skip("PowerPoint 窗口没有出现")
		}
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(10 * time.Second) // 等它注册好自动化对象（太早的话会另外启动一个 PowerPoint）
	tk := newTask(context.Background(), nil)
	defer tk.close()
	// 先把用户实例的 DisplayAlerts 设成一个「用户自己的值」（ppAlertsAll），看转换后会不会被还原
	var owned bool
	runCOM(tk, appPPT, time.Minute, func(a *comApp) error {
		owned = a.owned
		a.saved["DisplayAlerts"] = 2 // 用完后会被「还原」成 2
		return nil
	})
	if owned {
		t.Fatal("用户的 PowerPoint 不该算作我们启动的")
	}
	verify(t, convOne(t, fx.pptx, "pdf", nil))
	verify(t, convOne(t, fx.pptx, "pdf", nil))
	if !processAlive(pid) {
		t.Fatal("用户的 PowerPoint 被关掉了")
	}
	alerts := 0
	runCOM(tk, appPPT, time.Minute, func(a *comApp) error {
		alerts = variantIntOf(a.saved["DisplayAlerts"]) // 连接时读到的就是用户的设置
		return nil
	})
	if alerts != 2 {
		t.Errorf("DisplayAlerts 没有还原：%d", alerts)
	}
	Shutdown()
	if !processAlive(pid) {
		t.Fatal("Shutdown 把用户的 PowerPoint 关掉了")
	}
}

func variantIntOf(v any) int {
	i, _ := v.(int)
	return i
}

// 很宽的图片转成 docx 后要缩小到版心宽度
func TestWideImageInMarkdown(t *testing.T) {
	needWord(t)
	dir := filepath.Join(chineseDir(t), "宽 图片")
	img := image.NewRGBA(image.Rect(0, 0, 3000, 300))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	writeFile(t, filepath.Join(dir, "宽 图.png"), b.Bytes())
	md := writeFile(t, filepath.Join(dir, "宽图 文档.md"), []byte("# 宽图\n\n![](宽%20图.png)\n"))
	d := convOne(t, md, "docx", nil)
	doc := zipPart(t, d, "word/document.xml")
	m := regexp.MustCompile(`<wp:extent cx="(\d+)"`).FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("docx 里没有图片")
	}
	cx, _ := strconv.Atoi(m[1])
	if inch := float64(cx) / 914400; inch > 6.6 {
		t.Errorf("图片宽 %.1f 英寸，超出了页面", inch)
	}
	if rels := zipPart(t, d, "word/_rels/document.xml.rels"); strings.Contains(rels, `TargetMode="External"`) && strings.Contains(rels, "image") {
		t.Errorf("图片还是链接到外部文件：%s", rels)
	}
	// 改过的 docx 还能被 Word 正常打开，图片还在
	tk := newTask(context.Background(), nil)
	defer tk.close()
	var shapes, typ int
	err := runCOM(tk, appWord, time.Minute, func(a *comApp) error {
		docs, _ := a.app.mustSub("Documents")
		defer docs.release()
		doc, err := docs.mustSub("Open", d, false, true, false, bogusPW, bogusPW, false, missing, missing, missing, missing, false)
		if err != nil {
			return err
		}
		defer func() { doc.call("Close", 0); doc.release() }()
		is, _ := doc.mustSub("InlineShapes")
		defer is.release()
		shapes, _ = is.int("Count")
		if shapes > 0 {
			s, _ := is.mustSub("Item", 1)
			typ, _ = s.int("Type")
			s.release()
		}
		return nil
	})
	if err != nil || shapes != 1 || typ != 3 { // wdInlineShapePicture
		t.Errorf("Word 重新打开：%d 张图片，类型 %d，%v", shapes, typ, err)
	}
}

// 空闲一段时间后自动退出 Word
func TestIdleQuit(t *testing.T) {
	fx := fixtures(t)
	old := idleQuit
	idleQuit = 2 * time.Second
	defer func() { idleQuit = old }()
	verify(t, convOne(t, fx.docx, "pdf", nil))
	tk := newTask(context.Background(), nil)
	defer tk.close()
	var pid uint32
	runCOM(tk, appWord, time.Minute, func(a *comApp) error { pid = a.pid; return nil })
	if pid == 0 {
		t.Fatal("不知道 Word 的进程号")
	}
	deadline := time.Now().Add(20 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatal("空闲后 Word 没有退出")
		}
		time.Sleep(200 * time.Millisecond)
	}
	idleQuit = old
	start := time.Now()
	verify(t, convOne(t, fx.docx, "pdf", nil))
	t.Logf("Word 退出后再转换（冷启动）：%.1f 秒", time.Since(start).Seconds())
}

// 放在最后：Shutdown 之后不能留下我们启动的 Office 进程
func TestZZShutdownLeavesNoProcesses(t *testing.T) {
	needWord(t)
	start := time.Now()
	Shutdown()
	Shutdown() // 可以重复调用
	t.Logf("Shutdown 用了 %.1f 秒", time.Since(start).Seconds())
	deadline := time.Now().Add(15 * time.Second)
	for {
		var stray []string
		for _, e := range officeExes {
			for pid := range processes(e) {
				if !preexisting[e][pid] {
					stray = append(stray, fmt.Sprintf("%s(%d)", e, pid))
				}
			}
		}
		if len(stray) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Shutdown 后还有残留进程：%v", stray)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, e := range officeExes {
		for pid := range preexisting[e] {
			if !processAlive(pid) {
				t.Errorf("用户原来的 %s(%d) 不见了", e, pid)
			}
		}
	}
	// 不能在用户的 Office 里留下崩溃记录、文档恢复记录
	for k := appWord; k < numApps; k++ {
		for item := range snapshotResiliency(k) {
			path, name, ok := strings.Cut(item, "|")
			if !ok {
				continue
			}
			key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			b, _, _ := key.GetBinaryValue(name)
			key.Close()
			low := strings.ToLower(string(b))
			for _, marker := range []string{tempMarker, "办公 转换 测试", "测试 文件夹"} {
				if strings.Contains(low, string(utf16Bytes(marker))) {
					t.Errorf("%s 留下了记录：%s", regAppName[k], item)
				}
			}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Word")); err == nil {
		for _, e := range ents {
			for _, name := range []string{"测试 文档", "网页 文件", "说明 文档", "GBK 文本", "网页 存档", "宽图 文档", "src", "page"} {
				if strings.HasSuffix(e.Name(), ".asd") && strings.Contains(e.Name(), name) {
					t.Errorf("留下了自动恢复文件：%s", e.Name())
				}
			}
		}
	}
}
