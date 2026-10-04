package office

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 不需要 Office 的测试：编码、CSV、Markdown、表格提取、转换矩阵。

// chineseDir 建一个带中文和空格的测试文件夹
func chineseDir(t *testing.T) string {
	d := filepath.Join(t.TempDir(), "测试 文件夹 一")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func gbk(s string) []byte {
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		panic(err)
	}
	return b
}

func writeFile(t *testing.T, p string, b []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func pngBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 2), uint8(y * 4), 200, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// runJob 跑一次转换，返回输出文件
func runJob(t *testing.T, fn conv.RunFunc, in string, opt conv.Options) ([]string, error) {
	t.Helper()
	return runJobCtx(context.Background(), t, fn, in, opt)
}

func runJobCtx(ctx context.Context, t *testing.T, fn conv.RunFunc, in string, opt conv.Options) ([]string, error) {
	t.Helper()
	out := filepath.Join(filepath.Dir(in), "输出 结果")
	os.MkdirAll(out, 0o755)
	var notes []string
	j := conv.NewJob([]string{in}, "doc", opt, out, func(p conv.Progress) {
		if p.Note != "" && (len(notes) == 0 || notes[len(notes)-1] != p.Note) {
			notes = append(notes, p.Note)
		}
	})
	start := time.Now()
	err := fn(ctx, j)
	j.Finish(err != nil)
	t.Logf("%s → %v（%.1f 秒）进度：%s", filepath.Base(in), err, time.Since(start).Seconds(), strings.Join(notes, " | "))
	if err != nil {
		return nil, err
	}
	return j.Outputs(), nil
}

func TestCanConvert(t *testing.T) {
	yes := map[string][]string{
		"a.docx": {"pdf", "docx", "doc", "rtf", "odt", "txt", "html", ".PDF", "htm"},
		"a.wps":  {"pdf", "docx"},
		"a.xlsx": {"pdf", "xlsx", "xls", "csv", "ods", "html"},
		"a.et":   {"xlsx", "csv"},
		"a.csv":  {"xlsx", "xls", "ods", "pdf", "html", "csv"},
		"a.tsv":  {"csv", "xlsx"},
		"a.pptx": {"pdf", "pptx", "ppt", "odp", "mp4"},
		"a.dps":  {"pdf", "mp4"},
		"a.txt":  {"pdf", "docx", "html"},
		"a.md":   {"html", "pdf", "docx"},
		"a.html": {"pdf", "docx", "txt"},
		"a.mht":  {"pdf"},
		"a.pdf":  {"docx", "xlsx"},
	}
	no := map[string][]string{
		"a.docx": {"xlsx", "mp4", "pptx", "csv"},
		"a.xlsx": {"docx", "txt", "mp4"},
		"a.pptx": {"docx", "xlsx", "html", "txt"},
		"a.txt":  {"txt", "xlsx"},
		"a.md":   {"md", "txt"},
		"a.pdf":  {"pdf", "pptx"},
		"a.jpg":  {"pdf"},
	}
	for in, list := range yes {
		for _, to := range list {
			if !CanConvert(in, to) {
				t.Errorf("CanConvert(%s, %s) 应该是 true", in, to)
			}
		}
	}
	for in, list := range no {
		for _, to := range list {
			if CanConvert(in, to) {
				t.Errorf("CanConvert(%s, %s) 应该是 false", in, to)
			}
		}
	}
}

func TestInferCell(t *testing.T) {
	cases := []struct {
		in   string
		kind cellKind
		val  any
	}{
		{"", cellEmpty, nil},
		{"123", cellNumber, int64(123)},
		{"-45.5", cellNumber, -45.5},
		{"0", cellNumber, int64(0)},
		{"0.25", cellNumber, 0.25},
		{"00123", cellNumText, "00123"},
		{"013800138000", cellNumText, "013800138000"},
		{"110101199003071234", cellNumText, "110101199003071234"}, // 18 位身份证号
		{"1234567890123456", cellNumText, "1234567890123456"},     // 16 位
		{"123456789012345", cellNumber, int64(123456789012345)},   // 15 位还能精确保存
		{`="0042"`, cellNumText, "0042"},
		{"1,234,567.50", cellThousands, 1234567.5},
		{"12.5%", cellPercent, 0.125},
		{"1e5", cellText, "1e5"},
		{"+86 13800138000", cellText, "+86 13800138000"},
		{" 12", cellText, " 12"},
		{"你好", cellText, "你好"},
		{"2024-01-05", cellDate, time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)},
		{"2024/1/5 13:45", cellDate, time.Date(2024, 1, 5, 13, 45, 0, 0, time.UTC)},
		{"2024年3月8日", cellDate, time.Date(2024, 3, 8, 0, 0, 0, 0, time.UTC)},
		{"2024-02-30", cellText, "2024-02-30"},
		{"2024-01/05", cellText, "2024-01/05"},
	}
	for _, c := range cases {
		v := inferCell(c.in)
		if v.kind != c.kind {
			t.Errorf("inferCell(%q).kind = %v，应该是 %v", c.in, v.kind, c.kind)
			continue
		}
		if c.val != nil && v.value != c.val {
			if tm, ok := c.val.(time.Time); !ok || !tm.Equal(v.value.(time.Time)) {
				t.Errorf("inferCell(%q).value = %#v，应该是 %#v", c.in, v.value, c.val)
			}
		}
	}
}

func TestDetectEncoding(t *testing.T) {
	text := "第一行：你好，世界\r\nsecond line 123\r\n"
	u16, _ := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(text))
	u16be, _ := unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(text))
	u16raw, _ := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().Bytes([]byte("plain ascii text in utf16 without bom"))
	cases := []struct {
		b    []byte
		want textEncoding
	}{
		{[]byte(text), encUTF8},
		{append(append([]byte(nil), bomUTF8...), text...), encUTF8BOM},
		{gbk(text), encGBK},
		{u16, encUTF16LE},
		{u16be, encUTF16BE},
		{u16raw, encUTF16LE},
	}
	for i, c := range cases {
		if got := detectEncoding(c.b); got != c.want {
			t.Errorf("第 %d 个：%v，应该是 %v", i, got, c.want)
		}
		s, _ := decodeText(c.b)
		if c.want != encUTF16LE || i != 5 {
			if s != text {
				t.Errorf("第 %d 个解码结果不对：%q", i, s)
			}
		}
		// 文件版本（大文件分块检查）
		p := filepath.Join(t.TempDir(), "x.txt")
		os.WriteFile(p, c.b, 0o644)
		if got, err := detectFileEncoding(p); err != nil || got != c.want {
			t.Errorf("第 %d 个（文件）：%v %v", i, got, err)
		}
	}
	// 前面 300KB 都是英文、后面才出现 GBK 中文：也要认出 GBK
	big := append(bytes.Repeat([]byte("abc,123\r\n"), 40000), gbk("中文,结尾\r\n")...)
	p := filepath.Join(t.TempDir(), "big.csv")
	os.WriteFile(p, big, 0o644)
	if got, _ := detectFileEncoding(p); got != encGBK {
		t.Errorf("大文件：%v，应该是 GBK", got)
	}
	// UTF-8 中文正好被块边界切开也不能误判
	pad := bytes.Repeat([]byte("a"), 256*1024-1)
	p2 := filepath.Join(t.TempDir(), "edge.txt")
	os.WriteFile(p2, append(pad, []byte("中文结尾")...), 0o644)
	if got, _ := detectFileEncoding(p2); got != encUTF8 {
		t.Errorf("块边界：%v，应该是 UTF-8", got)
	}
}

const csvText = "编号,姓名,年龄,金额,日期,手机,身份证,比例,备注\r\n" +
	"00123,张三,28,1234.5,2024-01-05,13800138000,110101199003071234,12.5%,\"含,逗号\"\r\n" +
	"00456,李四,35,\"1,000\",2024/3/8,013912345678,11010119900307123X,50%,\"多行\r\n文本\"\r\n" +
	"789,王五,,0.25,2024年12月31日,,,,普通\r\n"

const tsvText = "编号\t姓名\t年龄\t金额\t日期\t手机\t身份证\t比例\t备注\r\n" +
	"00123\t张三\t28\t1234.5\t2024-01-05\t13800138000\t110101199003071234\t12.5%\t含,逗号\r\n" +
	"00456\t李四\t35\t1,000\t2024/3/8\t013912345678\t11010119900307123X\t50%\t\"多行\r\n文本\"\r\n" +
	"789\t王五\t\t0.25\t2024年12月31日\t\t\t\t普通\r\n"

func checkCSVWorkbook(t *testing.T, p string) {
	t.Helper()
	f, err := excelize.OpenFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sheet := f.GetSheetList()[0]
	get := func(cell string) string {
		v, _ := f.GetCellValue(sheet, cell, excelize.Options{RawCellValue: true})
		return v
	}
	typ := func(cell string) excelize.CellType {
		ct, _ := f.GetCellType(sheet, cell)
		return ct
	}
	if get("A2") != "00123" || get("A3") != "00456" {
		t.Errorf("前导 0 丢了：%q %q", get("A2"), get("A3"))
	}
	if get("B2") != "张三" || get("I2") != "含,逗号" || get("I3") != "多行\n文本" {
		t.Errorf("中文或引号字段不对：%q %q %q", get("B2"), get("I2"), get("I3"))
	}
	if get("C2") != "28" || typ("C2") == excelize.CellTypeSharedString || typ("C2") == excelize.CellTypeInlineString {
		t.Errorf("年龄应该是数字：%q 类型 %v", get("C2"), typ("C2"))
	}
	if get("D3") != "1000" {
		t.Errorf("千分位数字：%q", get("D3"))
	}
	if get("F2") != "13800138000" || get("F3") != "013912345678" {
		t.Errorf("手机号：%q %q", get("F2"), get("F3"))
	}
	if get("G2") != "110101199003071234" || get("G3") != "11010119900307123X" {
		t.Errorf("身份证号：%q %q", get("G2"), get("G3"))
	}
	if get("E2") != "45296" { // 2024-01-05 的序列号
		t.Errorf("日期应该变成 Excel 日期：%q", get("E2"))
	}
	if v, _ := f.GetCellValue(sheet, "E2"); v != "2024-01-05" {
		t.Errorf("日期显示：%q", v)
	}
	if v, _ := f.GetCellValue(sheet, "H2"); v != "12.5%" {
		t.Errorf("百分比显示：%q", v)
	}
}

func TestCSVToXlsx(t *testing.T) {
	dir := chineseDir(t)
	for name, data := range map[string][]byte{
		"中文 GBK 表格.csv":    gbk(csvText),
		"带 BOM 的 UTF8.csv": append(append([]byte(nil), bomUTF8...), csvText...),
		"无BOM UTF8.csv":    []byte(csvText),
		"分号.csv":           []byte(strings.ReplaceAll(strings.ReplaceAll(csvText, ",", ";"), `"1;000"`, `"1,000"`)),
		"制表符.tsv":          []byte(tsvText),
	} {
		t.Run(name, func(t *testing.T) {
			in := writeFile(t, filepath.Join(dir, name), data)
			if strings.Contains(name, "分号") {
				// 分号分隔时「含,逗号」本来就不需要引号，换回来
				b := bytes.ReplaceAll(data, []byte("\"含;逗号\""), []byte("\"含,逗号\""))
				os.WriteFile(in, b, 0o644)
			}
			outs, err := runJob(t, Convert("xlsx"), in, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(outs) != 1 || filepath.Ext(outs[0]) != ".xlsx" {
				t.Fatalf("输出：%v", outs)
			}
			checkCSVWorkbook(t, outs[0])
		})
	}
}

func TestCSVReencode(t *testing.T) {
	dir := chineseDir(t)
	in := writeFile(t, filepath.Join(dir, "老 Excel 导出.tsv"), gbk("编号\t名称\r\n00123\t中文,带逗号\r\n"))
	outs, err := runJob(t, Convert("csv"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(outs[0])
	want := "\xEF\xBB\xBF编号,名称\r\n00123,\"中文,带逗号\"\r\n"
	if string(b) != want {
		t.Errorf("得到 %q", b)
	}
}

// makeXlsx 用 excelize 生成两个工作表的测试表格
func makeXlsx(t *testing.T, p string) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
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
	dateStyle, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	f.SetCellStyle("销售", "E2", "E4", dateStyle)
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "#C00000"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#FFF2CC"}}, Alignment: &excelize.Alignment{Horizontal: "center"}})
	f.SetCellStyle("销售", "A1", "F1", bold)
	f.SetCellFormula("销售", "C5", "SUM(C2:C4)")
	f.SetCellValue("销售", "C5", 210) // 公式的缓存值（excelize 不计算）
	f.SetCellValue("销售", "A5", "合计")
	f.MergeCell("销售", "A5", "B5")
	f.SetColWidth("销售", "B", "B", 18)
	f.NewSheet("说明")
	f.SetCellValue("说明", "A1", "这是第二个工作表")
	f.SetCellValue("说明", "B3", "中间有空行")
	f.NewSheet("空表")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestXlsxToCSVAndHTMLWithoutOffice(t *testing.T) {
	old := forceEngine
	forceEngine = "none"
	defer func() { forceEngine = old }()
	dir := chineseDir(t)
	in := makeXlsx(t, filepath.Join(dir, "销售 报表.xlsx"))
	outs, err := runJob(t, Convert("csv"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 {
		t.Fatalf("应该有两个 CSV（空表跳过）：%v", outs)
	}
	names := []string{filepath.Base(outs[0]), filepath.Base(outs[1])}
	if names[0] != "销售 报表-销售.csv" || names[1] != "销售 报表-说明.csv" {
		t.Errorf("文件名：%v", names)
	}
	b, _ := os.ReadFile(outs[0])
	s := string(b)
	if !strings.HasPrefix(s, "\xEF\xBB\xBF") {
		t.Error("应该带 BOM")
	}
	for _, want := range []string{"编号,产品,数量,单价,日期,备注\r\n", "00123,苹果,10,3.5,2024/1/5,第一批\r\n", "\"含,逗号\"", "合计,,210,,,\r\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("CSV 里没有 %q：\n%s", want, s)
		}
	}
	b2, _ := os.ReadFile(outs[1])
	if string(b2) != "\xEF\xBB\xBF这是第二个工作表,\r\n,\r\n,中间有空行\r\n" {
		t.Errorf("第二个表：%q", b2)
	}
	// CSV 再转回 xlsx：前导 0 和中文都还在
	back, err := runJob(t, Convert("xlsx"), outs[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := excelize.OpenFile(back[0])
	if v, _ := f.GetCellValue(f.GetSheetList()[0], "A2"); v != "00123" {
		t.Errorf("往返后前导 0：%q", v)
	}
	if v, _ := f.GetCellValue(f.GetSheetList()[0], "B3"); v != "香蕉" {
		t.Errorf("往返后中文：%q", v)
	}
	f.Close()

	// 网页
	outs, err = runJob(t, Convert("html"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := os.ReadFile(outs[0])
	hs := string(h)
	for _, want := range []string{"<h2>销售</h2>", "<h2>说明</h2>", "苹果", "00123", "colspan=\"2\"", "font-weight:bold", "background:#fff2cc", "2024/1/5", "class=\"n\""} {
		if !strings.Contains(hs, want) {
			t.Errorf("网页里没有 %q", want)
		}
	}
	if strings.Contains(hs, "<h2>空表</h2>") {
		t.Error("空表不该出现")
	}
}

func TestNoOfficeBranch(t *testing.T) {
	old := forceEngine
	forceEngine = "none"
	defer func() { forceEngine = old }()
	dir := chineseDir(t)
	in := writeFile(t, filepath.Join(dir, "文档.docx"), []byte("PK\x03\x04 不是真的 docx"))
	_, err := runJob(t, Convert("pdf"), in, nil)
	var ue *conv.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "需要安装 Microsoft Office、WPS 或 LibreOffice") || !strings.Contains(ue.Detail, "libreoffice.org") {
		t.Errorf("没装 Office 时应该提示安装：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "输出 结果", "文档.pdf")); err == nil {
		t.Error("失败时不该留下输出文件")
	}
	pdf := writeFile(t, filepath.Join(dir, "a.pdf"), []byte("%PDF-1.4\n"))
	if _, err := runJob(t, PDFToWord(), pdf, nil); !errors.As(err, &ue) {
		t.Errorf("PDF 转 Word：%v", err)
	}
	ppt := writeFile(t, filepath.Join(dir, "演示.pptx"), []byte("PK\x03\x04"))
	if _, err := runJob(t, Convert("mp4"), ppt, nil); err == nil || !strings.Contains(err.Error(), "PowerPoint") {
		t.Errorf("转视频：%v", err)
	}
	// 不需要 Office 的仍然能用
	csvIn := writeFile(t, filepath.Join(dir, "数据.csv"), gbk(csvText))
	if _, err := runJob(t, Convert("xlsx"), csvIn, nil); err != nil {
		t.Errorf("CSV 转 xlsx 不需要 Office：%v", err)
	}
	mdIn := writeFile(t, filepath.Join(dir, "说明.md"), []byte("# 标题\n\n正文"))
	if _, err := runJob(t, Convert("html"), mdIn, nil); err != nil {
		t.Errorf("Markdown 转网页不需要 Office：%v", err)
	}
	txtIn := writeFile(t, filepath.Join(dir, "笔记.txt"), gbk("中文笔记\r\n第二行"))
	if outs, err := runJob(t, Convert("html"), txtIn, nil); err != nil {
		t.Errorf("文本转网页不需要 Office：%v", err)
	} else if b, _ := os.ReadFile(outs[0]); !strings.Contains(string(b), "中文笔记\n第二行") {
		t.Errorf("文本网页内容：%s", b)
	}
	htmlIn := writeFile(t, filepath.Join(dir, "页面.html"), []byte(`<html><head><meta charset="gbk"><style>p{}</style><script>var x=1</script></head><body><h1>`+string(gbk("标题"))+`</h1><p>`+string(gbk("第一段 文字"))+`</p><table><tr><td>A</td><td>B</td></tr></table><ul><li>`+string(gbk("一"))+`</li><li>`+string(gbk("二"))+`</li></ul></body></html>`))
	if outs, err := runJob(t, Convert("txt"), htmlIn, nil); err != nil {
		t.Errorf("网页转文本不需要 Office：%v", err)
	} else {
		b, _ := os.ReadFile(outs[0])
		want := "\xEF\xBB\xBF标题\r\n\r\n第一段 文字\r\n\r\nA\tB\r\n\r\n• 一\r\n• 二\r\n"
		if string(b) != want {
			t.Errorf("网页转文本：%q", b)
		}
	}
	if HasWord() && forceEngine == "none" {
		// HasWord 只反映安装情况，不受测试开关影响
		t.Log("HasWord =", HasWord())
	}
}

func TestMarkdownHTML(t *testing.T) {
	dir := chineseDir(t)
	writeFile(t, filepath.Join(dir, "图片 目录", "示例 图.png"), pngBytes())
	md := "---\ntitle: 前言标题\ntags: [a]\n---\n" +
		"# 使用说明\n\n第一行中文\n第二行中文（中文换行不该多出空格）\n\n" +
		"| 名称 | 数量 |\n|---|---:|\n| 苹果 | 10 |\n| ~~香蕉~~ | 20 |\n\n" +
		"```go\nfunc main() {\n\tfmt.Println(\"你好 <世界>\")\n}\n```\n\n" +
		"- [x] 已完成\n- [ ] 未完成\n\n" +
		"![示例](图片%20目录/示例%20图.png)\n\n<img src=\"图片 目录/示例 图.png\" width=\"50\">\n\n" +
		"网址 https://example.com 自动变链接。脚注[^1]\n\n[^1]: 脚注内容\n"
	in := writeFile(t, filepath.Join(dir, "说明 文档.md"), []byte(md))
	outs, err := runJob(t, Convert("html"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(outs[0])
	s := string(b)
	for _, want := range []string{
		`<meta charset="utf-8">`, "<title>使用说明</title>", "<table>", "<del>香蕉</del>", `<pre><code class="language-go">`,
		"&lt;世界&gt;", `type="checkbox"`, `<a href="https://example.com">`, "第一行中文第二行中文", "font-family",
		"Microsoft YaHei", "@page", "脚注内容",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("网页里没有 %q", want)
		}
	}
	if strings.Count(s, `src="data:image/png;base64,`) != 2 {
		t.Errorf("两张本地图片都应该嵌进网页：%d", strings.Count(s, "data:image/png"))
	}
	if strings.Contains(s, "tags:") {
		t.Error("YAML 元数据应该去掉")
	}
	// 给 Edge / Word 用的版本：图片换成 file:/// 地址
	page, err := renderMarkdown(in, imgFileURL, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `src="file:///`) || !strings.Contains(string(page), "%E5%9B%BE%E7%89%87%20%E7%9B%AE%E5%BD%95") {
		t.Errorf("file 地址不对：%s", page)
	}
}

func TestReadDocxTables(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006">
<w:body>
<w:p><w:r><w:t>表格前的段落</w:t></w:r></w:p>
<w:tbl>
 <w:tr><w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>合并标题</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>C</w:t></w:r></w:p></w:tc></w:tr>
 <w:tr><w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr><w:p><w:r><w:t>纵向</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>00123</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>1,234.5</w:t></w:r></w:p></w:tc></w:tr>
 <w:tr><w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p/></w:tc><w:tc><w:p><w:r><w:t>第一行</w:t></w:r></w:p><w:p><w:r><w:t>第二行</w:t></w:r></w:p></w:tc><w:tc>
   <w:tbl><w:tr><w:tc><w:p><w:r><w:t>嵌套</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:p/></w:tc></w:tr>
</w:tbl>
<w:p><w:r><mc:AlternateContent><mc:Choice><w:t>选择</w:t></mc:Choice><mc:Fallback><w:t>重复</w:t></mc:Fallback></mc:AlternateContent></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p/></w:tc></w:tr></w:tbl>
</w:body></w:document>`
	tables, paras, err := parseDocumentXML(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 || len(tables[0]) != 3 {
		t.Fatalf("表格：%d 个，第一个 %d 行", len(tables), len(tables[0]))
	}
	if strings.Join(paras, "|") != "表格前的段落|选择" {
		t.Errorf("段落：%q", paras)
	}
	if c := tables[0][2][1]; c.text != "第一行\n第二行" {
		t.Errorf("多段落单元格：%q", c.text)
	}
	if c := tables[0][2][2]; c.text != "嵌套" {
		t.Errorf("嵌套表格：%q", c.text)
	}
	out := filepath.Join(chineseDir(t), "表格.xlsx")
	n, err := writeTablesXlsx(tables, paras, out)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	f, _ := excelize.OpenFile(out)
	defer f.Close()
	if l := f.GetSheetList(); len(l) != 1 || l[0] != "表格1" {
		t.Errorf("工作表：%v", l)
	}
	get := func(c string) string {
		v, _ := f.GetCellValue("表格1", c, excelize.Options{RawCellValue: true})
		return v
	}
	if get("A1") != "合并标题" || get("C1") != "C" || get("A2") != "纵向" || get("B2") != "00123" || get("C2") != "1234.5" || get("B3") != "第一行\n第二行" {
		t.Errorf("单元格：%q %q %q %q %q %q", get("A1"), get("C1"), get("A2"), get("B2"), get("C2"), get("B3"))
	}
	mc, _ := f.GetMergeCells("表格1")
	var ms []string
	for _, m := range mc {
		ms = append(ms, m.GetStartAxis()+":"+m.GetEndAxis())
	}
	got := strings.Join(ms, ",")
	if !strings.Contains(got, "A1:B1") || !strings.Contains(got, "A2:A3") {
		t.Errorf("合并单元格：%s", got)
	}
	// 没有表格时：段落放进 A 列
	out2 := filepath.Join(chineseDir(t), "文字.xlsx")
	if n, err := writeTablesXlsx(nil, []string{"第一段", "第二段"}, out2); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	f2, _ := excelize.OpenFile(out2)
	defer f2.Close()
	if v, _ := f2.GetCellValue("文字", "A2"); v != "第二段" {
		t.Errorf("段落：%q", v)
	}
}

func TestSniffAndExt(t *testing.T) {
	dir := chineseDir(t)
	ole := writeFile(t, filepath.Join(dir, "a.wps"), append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 100)...))
	if officeExt(ole, appWord) != ".doc" {
		t.Error("ole 的 wps 应该当 doc")
	}
	h := writeFile(t, filepath.Join(dir, "网页.xls"), []byte("<html><body><table><tr><td>1</td></tr></table></body></html>"))
	if officeExt(h, appExcel) != ".htm" {
		t.Error("网页伪装的 xls 应该当 htm")
	}
	x := filepath.Join(dir, "真的是 xlsx.et")
	os.Rename(makeXlsx(t, filepath.Join(dir, "c.xlsx")), x)
	if officeExt(x, appExcel) != ".xlsx" {
		t.Errorf("zip 的 et：%s", officeExt(x, appExcel))
	}
	if !excelizeReadable(makeXlsx(t, filepath.Join(dir, "b.xlsx"))) {
		t.Error("xlsx 应该能直接读")
	}
}

func TestFileURL(t *testing.T) {
	cases := map[string]string{
		`C:\a b\中文.html`:       "file:///C:/a%20b/%E4%B8%AD%E6%96%87.html",
		`\\server\share\x.htm`: "file://server/share/x.htm",
	}
	for in, want := range cases {
		if got := fileURL(in); got != want {
			t.Errorf("fileURL(%q) = %q，应该是 %q", in, got, want)
		}
	}
}

func TestLibreOfficeArgs(t *testing.T) {
	args := loArgs(`C:\Users\张三\AppData\Local\omni-convert\libreoffice`, `C:\tmp\src1.pdf`, `C:\tmp\lo2`, "docx", openOpts{pdf: true})
	s := strings.Join(args, " ")
	for _, want := range []string{"-env:UserInstallation=file:///C:/Users/%E5%BC%A0%E4%B8%89/", "--headless", "--infilter=writer_pdf_import", "--convert-to docx:MS Word 2007 XML", "--outdir C:\\tmp\\lo2 C:\\tmp\\src1.pdf"} {
		if !strings.Contains(s, want) {
			t.Errorf("参数里没有 %q：%s", want, s)
		}
	}
	for _, to := range []string{"pdf", "docx", "doc", "rtf", "odt", "txt", "html", "xlsx", "xls", "ods", "pptx", "ppt", "odp"} {
		if loFilters[to] == "" {
			t.Errorf("LibreOffice 缺少 %s 的过滤器", to)
		}
	}
}

func TestPrintableHTML(t *testing.T) {
	dir := chineseDir(t)
	page := `<!DOCTYPE html><html><head><meta http-equiv="Content-Type" content="text/html; charset=gb2312"><title>x</title></head><body><img src="a.png"></body></html>`
	in := writeFile(t, filepath.Join(dir, "页 面.htm"), []byte(page))
	tk := newTask(context.Background(), nil)
	defer tk.close()
	p, err := tk.printableHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	i := strings.Index(s, "charset=gb2312")
	j := strings.Index(s, "<base href=\"file:///")
	k := strings.Index(s, "@page")
	if i < 0 || j < i || k < j {
		t.Errorf("插入位置不对：%s", s)
	}
	if !strings.Contains(s, "%E9%A1%B5%20%E9%9D%A2") && !strings.Contains(s, "/%E6%B5%8B%E8%AF%95%20") {
		t.Errorf("base 地址没有转义：%s", s)
	}
}

// 假的 soffice：按 --convert-to 的扩展名把输入复制到 --outdir（PDF 写个假的文件头），并记下参数
const fakeSoffice = `package main

import (
	"os"
	"path/filepath"
	"strings"
)

func main() {
	args := os.Args[1:]
	os.WriteFile(filepath.Join(os.Getenv("FAKE_LOG_DIR"), "args.txt"), []byte(strings.Join(args, "\n")), 0o644)
	var to, outdir string
	for i, a := range args {
		switch a {
		case "--convert-to":
			to = args[i+1]
		case "--outdir":
			outdir = args[i+1]
		}
	}
	in := args[len(args)-1]
	ext, _, _ := strings.Cut(to, ":")
	base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	data, _ := os.ReadFile(in)
	switch ext {
	case "pdf":
		data = []byte("%PDF-1.4 fake")
	case "html":
		os.WriteFile(filepath.Join(outdir, "pic.png"), []byte("png"), 0o644)
		data = []byte("<html><head><meta charset=\"utf-8\"></head><body>中文<img src=\"pic.png\"></body></html>")
	case "txt":
		data = []byte("没有BOM的文本\n第二行")
	}
	os.WriteFile(filepath.Join(outdir, base+"."+ext), data, 0o644)
}
`

func TestLibreOfficeEngineFake(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := chineseDir(t)
	src := writeFile(t, filepath.Join(dir, "fake", "main.go"), []byte(fakeSoffice))
	writeFile(t, filepath.Join(dir, "fake", "go.mod"), []byte("module fake\n\ngo 1.21\n"))
	exe := filepath.Join(dir, "fake", "soffice.exe")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = filepath.Dir(src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skip("编译假 soffice 失败：", err, string(out))
	}
	t.Setenv("FAKE_LOG_DIR", dir)
	Detect()
	oldLO, oldForce := detected.LibreOffice, forceEngine
	detected.LibreOffice, forceEngine = exe, "soffice"
	defer func() { detected.LibreOffice, forceEngine = oldLO, oldForce }()

	in := writeFile(t, filepath.Join(dir, "文档 一.docx"), []byte("PK\x03\x04 假的"))
	outs, err := runJob(t, Convert("pdf"), in, nil)
	if err != nil || !isPDFFile(outs[0]) {
		t.Fatalf("pdf：%v %v", outs, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if !strings.Contains(string(args), "--convert-to\npdf\n") || !strings.Contains(string(args), "-env:UserInstallation=file:///") {
		t.Errorf("参数：%s", args)
	}
	outs, err = runJob(t, Convert("html"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outs[0]); !strings.Contains(string(b), "data:image/png;base64,") {
		t.Errorf("LibreOffice 的网页图片要嵌进去：%s", b)
	}
	outs, err = runJob(t, Convert("txt"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outs[0]); string(b) != "\xEF\xBB\xBF没有BOM的文本\r\n第二行" {
		t.Errorf("文本：%q", b)
	}
	// PDF 转 Word：要用 writer_pdf_import 导入
	pdf := writeFile(t, filepath.Join(dir, "扫描.pdf"), []byte("%PDF-1.4"))
	if _, err := runJob(t, PDFToWord(), pdf, nil); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(filepath.Join(dir, "args.txt"))
	if !strings.Contains(string(args), "--infilter=writer_pdf_import") || !strings.Contains(string(args), "docx:MS Word 2007 XML") {
		t.Errorf("PDF 转 Word 的参数：%s", args)
	}
	// 文本：先转成 UTF-8 再用 Text (encoded):UTF8 导入
	txt := writeFile(t, filepath.Join(dir, "笔记.txt"), gbk("中文"))
	if _, err := runJob(t, Convert("docx"), txt, nil); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(filepath.Join(dir, "args.txt"))
	if !strings.Contains(string(args), "--infilter=Text (encoded):UTF8") {
		t.Errorf("文本的参数：%s", args)
	}
	// 转视频不能用 LibreOffice
	ppt := writeFile(t, filepath.Join(dir, "演示.pptx"), []byte("PK\x03\x04"))
	if _, err := runJob(t, Convert("mp4"), ppt, nil); err == nil {
		t.Error("LibreOffice 不能转视频")
	}
}

func TestCSVRowOverflow(t *testing.T) {
	old := maxRows
	maxRows = 3
	defer func() { maxRows = old }()
	var b strings.Builder
	for i := 1; i <= 8; i++ {
		b.WriteString("第" + itoa(i) + "行," + itoa(i) + "\r\n")
	}
	in := writeFile(t, filepath.Join(chineseDir(t), "很长的 表格.csv"), []byte(b.String()))
	outs, err := runJob(t, Convert("xlsx"), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := excelize.OpenFile(outs[0])
	defer f.Close()
	l := f.GetSheetList()
	if len(l) != 3 || l[0] != "很长的 表格" || l[1] != "很长的 表格 (2)" || l[2] != "很长的 表格 (3)" {
		t.Fatalf("工作表：%q", l)
	}
	if v, _ := f.GetCellValue(l[2], "A2"); v != "第8行" {
		t.Errorf("最后一行：%q", v)
	}
}
