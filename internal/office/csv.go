package office

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/transform"

	"github.com/haoawake/omni-convert/internal/conv"
)

// CSV ↔ Excel，纯 Go 完成，不需要 Office。

const (
	maxCols      = 16384
	maxCellChars = 32767
)

// maxRows 是 Excel 每个工作表最多的行数，超过的部分接着写到下一个工作表（测试时可以改小）
var maxRows = 1048576

// csvSource 是一个打开的 CSV 文件
type csvSource struct {
	f     *os.File
	r     *csv.Reader
	enc   textEncoding
	delim rune
}

func (s *csvSource) Close() error { return s.f.Close() }

// openCSV 打开 CSV / TSV：自动判断编码和分隔符
func openCSV(path string) (*csvSource, error) {
	enc, err := detectFileEncoding(path)
	if err != nil {
		return nil, conv.Fail("没法读取文件", err.Error())
	}
	delim := ','
	if conv.Ext(path) == ".tsv" {
		delim = '\t'
	} else {
		delim = sniffDelimiter(path, enc)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, conv.Fail("没法读取文件", err.Error())
	}
	var rd io.Reader = bufio.NewReaderSize(f, 256*1024)
	if enc != encUTF8 {
		rd = transform.NewReader(rd, enc.decoder())
	}
	r := csv.NewReader(rd)
	r.Comma = delim
	r.FieldsPerRecord = -1 // 每行列数可以不一样
	r.LazyQuotes = true    // 容忍不规范的引号
	return &csvSource{f: f, r: r, enc: enc, delim: delim}, nil
}

// sniffDelimiter 看前几行，判断分隔符是逗号、制表符还是分号
func sniffDelimiter(path string, enc textEncoding) rune {
	f, err := os.Open(path)
	if err != nil {
		return ','
	}
	defer f.Close()
	head := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, head)
	text := string(head[:n])
	if enc != encUTF8 {
		if b, _, err := transform.Bytes(enc.decoder(), head[:n-incompleteTail(head[:n])]); err == nil {
			text = string(b)
		}
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 1 {
		lines = lines[:len(lines)-1] // 最后一行可能被截断
	}
	if len(lines) > 30 {
		lines = lines[:30]
	}
	best, bestScore := ',', 0
	for _, d := range []rune{',', '\t', ';'} {
		counts := map[int]int{}
		nonzero := 0
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			c := countOutsideQuotes(l, d)
			counts[c]++
			if c > 0 {
				nonzero++
			}
		}
		// 分数：大多数行里出现次数一致且不为 0
		mode, modeN := 0, 0
		for c, k := range counts {
			if c > 0 && k > modeN {
				mode, modeN = c, k
			}
		}
		score := modeN*10 + nonzero
		if mode > 0 && score > bestScore {
			best, bestScore = d, score
		}
	}
	return best
}

func countOutsideQuotes(line string, d rune) int {
	in := false
	n := 0
	for _, r := range line {
		switch {
		case r == '"':
			in = !in
		case r == d && !in:
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- 单元格类型推断

type cellKind int

const (
	cellEmpty cellKind = iota
	cellText
	cellNumText // 看起来像数字但必须保持文本（前导 0、超过 15 位）
	cellNumber
	cellPercent
	cellThousands
	cellDate
)

var (
	reNumber    = regexp.MustCompile(`^-?(\d+(\.\d+)?|\.\d+)$`)
	reThousands = regexp.MustCompile(`^-?\d{1,3}(,\d{3})+(\.\d+)?$`)
	rePercent   = regexp.MustCompile(`^-?\d+(\.\d+)?%$`)
	reDate      = regexp.MustCompile(`^(\d{4})([-/.])(\d{1,2})([-/.])(\d{1,2})(?:[ T](\d{1,2}):(\d{2})(?::(\d{2}))?)?$`)
	reDateCN    = regexp.MustCompile(`^(\d{4})年(\d{1,2})月(\d{1,2})日$`)
	reAllDigits = regexp.MustCompile(`^\d+$`)
)

// cellValue 是推断出来的单元格：值和数字格式
type cellValue struct {
	kind   cellKind
	value  any    // string / int64 / float64 / time.Time
	numFmt string // 自定义数字格式，空表示常规
}

// inferCell 像 Excel 打开 CSV 时那样猜单元格的类型，但不会弄丢前导 0 和长数字（身份证号、手机号、订单号）
func inferCell(s string) cellValue {
	if s == "" {
		return cellValue{kind: cellEmpty}
	}
	// Excel 常用的 ="00123" 写法：强制文本
	if len(s) > 3 && strings.HasPrefix(s, `="`) && strings.HasSuffix(s, `"`) {
		return cellValue{kind: cellNumText, value: s[2 : len(s)-1]}
	}
	t := strings.TrimSpace(s)
	if t != s || t == "" {
		return cellValue{kind: cellText, value: s}
	}
	switch {
	case reNumber.MatchString(t):
		digits := strings.TrimPrefix(t, "-")
		intPart, _, _ := strings.Cut(digits, ".")
		nd := len(strings.ReplaceAll(digits, ".", ""))
		if (len(intPart) > 1 && intPart[0] == '0') || nd > 15 {
			return cellValue{kind: cellNumText, value: s}
		}
		if !strings.Contains(t, ".") {
			if v, err := strconv.ParseInt(t, 10, 64); err == nil {
				return cellValue{kind: cellNumber, value: v}
			}
		}
		if v, err := strconv.ParseFloat(t, 64); err == nil && !math.IsInf(v, 0) {
			return cellValue{kind: cellNumber, value: v}
		}
	case reThousands.MatchString(t):
		plain := strings.ReplaceAll(t, ",", "")
		if len(strings.ReplaceAll(strings.TrimPrefix(plain, "-"), ".", "")) <= 15 {
			if v, err := strconv.ParseFloat(plain, 64); err == nil {
				f := "#,##0"
				if _, frac, ok := strings.Cut(plain, "."); ok {
					f += "." + strings.Repeat("0", len(frac))
				}
				return cellValue{kind: cellThousands, value: v, numFmt: f}
			}
		}
	case rePercent.MatchString(t):
		num := strings.TrimSuffix(t, "%")
		if v, err := strconv.ParseFloat(num, 64); err == nil {
			f := "0%"
			if _, frac, ok := strings.Cut(num, "."); ok {
				f = "0." + strings.Repeat("0", len(frac)) + "%"
			}
			return cellValue{kind: cellPercent, value: v / 100, numFmt: f}
		}
	}
	if m := reDate.FindStringSubmatch(t); m != nil && m[2] == m[4] {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[3])
		d, _ := strconv.Atoi(m[5])
		hh, mm, ss := 0, 0, 0
		hasTime, hasSec := m[6] != "", m[8] != ""
		if hasTime {
			hh, _ = strconv.Atoi(m[6])
			mm, _ = strconv.Atoi(m[7])
			ss, _ = strconv.Atoi(m[8])
		}
		tm := time.Date(y, time.Month(mo), d, hh, mm, ss, 0, time.UTC)
		if y >= 1900 && tm.Year() == y && int(tm.Month()) == mo && tm.Day() == d && hh < 24 && mm < 60 && ss < 60 {
			sep := m[2]
			f := "yyyy" + sep + "mm" + sep + "dd"
			if len(m[3]) == 1 || len(m[5]) == 1 {
				f = "yyyy" + sep + "m" + sep + "d"
			}
			if sep == "/" || sep == "." {
				f = strings.ReplaceAll(f, sep, `\`+sep)
			}
			if hasTime {
				f += " hh:mm"
				if hasSec {
					f += ":ss"
				}
			}
			return cellValue{kind: cellDate, value: tm, numFmt: f}
		}
	}
	if m := reDateCN.FindStringSubmatch(t); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		tm := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		if y >= 1900 && int(tm.Month()) == mo && tm.Day() == d {
			return cellValue{kind: cellDate, value: tm, numFmt: `yyyy"年"m"月"d"日"`}
		}
	}
	if utf8.RuneCountInString(s) > maxCellChars {
		s = string([]rune(s)[:maxCellChars])
	}
	return cellValue{kind: cellText, value: s}
}

// displayWidth 估算文字在 Excel 里占几个字符宽（中文算 2 个）
func displayWidth(s string) float64 {
	w := 0.0
	for _, r := range s {
		switch {
		case r == '\n':
			return w
		case r > 0x2E80 && (unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || r >= 0xFF00 && r <= 0xFFEF || r >= 0x3000 && r <= 0x303F):
			w += 2
		default:
			w++
		}
	}
	return w
}

// ---------------------------------------------------------------- 写 xlsx

// styleCache 按数字格式缓存样式
type styleCache struct {
	f      *excelize.File
	ids    map[string]int
	border bool
}

func newStyleCache(f *excelize.File, border bool) *styleCache {
	return &styleCache{f: f, ids: map[string]int{}, border: border}
}

func (c *styleCache) get(numFmt string, bold bool) int {
	key := fmt.Sprintf("%s|%v", numFmt, bold)
	if id, ok := c.ids[key]; ok {
		return id
	}
	st := &excelize.Style{}
	if numFmt != "" {
		f := numFmt
		st.CustomNumFmt = &f
	}
	if bold {
		st.Font = &excelize.Font{Bold: true}
	}
	if c.border {
		b := []excelize.Border{}
		for _, side := range []string{"left", "right", "top", "bottom"} {
			b = append(b, excelize.Border{Type: side, Color: "#A6A6A6", Style: 1})
		}
		st.Border = b
		st.Alignment = &excelize.Alignment{Vertical: "center", WrapText: true}
	}
	id, err := c.f.NewStyle(st)
	if err != nil {
		id = 0
	}
	c.ids[key] = id
	return id
}

// cellFor 把推断结果变成 StreamWriter 用的单元格
func (c *styleCache) cellFor(v cellValue, bold bool) any {
	switch v.kind {
	case cellEmpty:
		if c.border {
			return excelize.Cell{StyleID: c.get("", bold)}
		}
		return nil
	case cellNumText:
		return excelize.Cell{StyleID: c.get("@", bold), Value: v.value}
	}
	if v.numFmt == "" && !bold && !c.border {
		return v.value
	}
	return excelize.Cell{StyleID: c.get(v.numFmt, bold), Value: v.value}
}

// sheetName 把文件名变成合法的工作表名
func sheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, "' ")
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	if s == "" || strings.EqualFold(s, "History") {
		s = "Sheet1"
	}
	return s
}

func colWidth(w float64) float64 {
	return math.Min(math.Max(w+2, 8), 60)
}

// csvToXlsx 把 CSV 写成 xlsx：数字变成数字、日期变成日期，前导 0 和长数字保持文本
func csvToXlsx(t *task, in, out string) error {
	src, err := openCSV(in)
	if err != nil {
		return err
	}
	defer src.Close()
	t.report(-1, "正在读取 CSV（"+src.enc.String()+"）…")

	f := excelize.NewFile()
	defer f.Close()
	name := sheetName(strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)))
	if name != "Sheet1" {
		f.SetSheetName("Sheet1", name)
	}
	styles := newStyleCache(f, false)

	// 先读前 500 行估算列宽（StreamWriter 要求先设列宽再写行）
	var head [][]string
	readErr := error(nil)
	for len(head) < 500 {
		rec, err := src.r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
		head = append(head, rec)
	}
	if readErr != nil && len(head) == 0 {
		return conv.Fail("CSV 文件格式不对", readErr.Error())
	}
	if len(head) == 0 {
		return conv.Fail("CSV 文件是空的", "")
	}
	var widths []float64
	for _, rec := range head {
		if len(rec) > maxCols {
			return conv.Fail(fmt.Sprintf("这个 CSV 有 %d 列，超过了 Excel 最多 %d 列的限制", len(rec), maxCols), "")
		}
		for i, s := range rec {
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			widths[i] = math.Max(widths[i], displayWidth(s))
		}
	}

	sheetNo := 1
	curName := name
	var sw *excelize.StreamWriter
	newSheet := func() error {
		if sw != nil {
			if err := sw.Flush(); err != nil {
				return err
			}
			sheetNo++
			curName = sheetName(fmt.Sprintf("%s (%d)", string([]rune(name)[:min(len([]rune(name)), 25)]), sheetNo))
			if _, err := f.NewSheet(curName); err != nil {
				return err
			}
		}
		var err error
		sw, err = f.NewStreamWriter(curName)
		if err != nil {
			return err
		}
		for i, w := range widths {
			if w > 0 {
				sw.SetColWidth(i+1, i+1, colWidth(w))
			}
		}
		return nil
	}
	if err := newSheet(); err != nil {
		return conv.Fail("没法生成 Excel 文件", err.Error())
	}
	row, total := 0, 0
	write := func(rec []string) error {
		if len(rec) > maxCols {
			return conv.Fail(fmt.Sprintf("这个 CSV 有 %d 列，超过了 Excel 最多 %d 列的限制", len(rec), maxCols), "")
		}
		if row == maxRows {
			if err := newSheet(); err != nil {
				return err
			}
			row = 0
		}
		row++
		total++
		vals := make([]any, len(rec))
		for i, s := range rec {
			vals[i] = styles.cellFor(inferCell(s), false)
		}
		cell, _ := excelize.CoordinatesToCellName(1, row)
		return sw.SetRow(cell, vals)
	}
	for _, rec := range head {
		if err := write(rec); err != nil {
			return err
		}
	}
	for readErr == nil {
		rec, err := src.r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return conv.Fail(fmt.Sprintf("CSV 第 %d 行格式不对", pe.Line), err.Error())
			}
			return conv.Fail("读取 CSV 出错", err.Error())
		}
		if err := write(rec); err != nil {
			return err
		}
		if total%20000 == 0 {
			if t.cancelled() {
				return conv.ErrCancelled
			}
			t.report(-1, fmt.Sprintf("已写入 %d 行…", total))
		}
	}
	if err := sw.Flush(); err != nil {
		return conv.Fail("没法生成 Excel 文件", err.Error())
	}
	if err := f.SaveAs(out); err != nil {
		return conv.Fail("没法保存 Excel 文件", err.Error())
	}
	return nil
}

// reencodeCSV 把 CSV / TSV 统一成逗号分隔、带 BOM 的 UTF-8（Excel 双击打开不会乱码）
func reencodeCSV(in, out string) error {
	src, err := openCSV(in)
	if err != nil {
		return err
	}
	defer src.Close()
	cw, err := newCSVWriter(out)
	if err != nil {
		return err
	}
	for {
		rec, err := src.r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			cw.close()
			return conv.Fail("CSV 文件格式不对", err.Error())
		}
		cw.w.Write(rec)
	}
	return cw.close()
}

// csvOut 写带 BOM 的 UTF-8 CSV，换行用 \r\n
type csvOut struct {
	f  *os.File
	bw *bufio.Writer
	w  *csv.Writer
}

func newCSVWriter(path string) (*csvOut, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, conv.Fail("没法写入输出文件", err.Error())
	}
	bw := bufio.NewWriterSize(f, 256*1024)
	bw.Write(bomUTF8)
	w := csv.NewWriter(bw)
	w.UseCRLF = true
	return &csvOut{f: f, bw: bw, w: w}, nil
}

func (c *csvOut) close() error {
	c.w.Flush()
	err := c.w.Error()
	if e := c.bw.Flush(); err == nil {
		err = e
	}
	if e := c.f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	return nil
}

// ---------------------------------------------------------------- xlsx → csv

// openXlsx 用适合中文用户的日期格式打开表格（显示值和 Excel 里看到的一致）
func openXlsx(path string) (*excelize.File, error) {
	return excelize.OpenFile(path, excelize.Options{
		ShortDatePattern: "yyyy/m/d",
		LongTimePattern:  "h:mm:ss",
		CultureInfo:      excelize.CultureNameZhCN,
	})
}

// sheetExtent 返回工作表的有效行数和列数（去掉末尾的空行空列）
func sheetExtent(f *excelize.File, sheet string) (rows, cols int, err error) {
	it, err := f.Rows(sheet)
	if err != nil {
		return 0, 0, err
	}
	defer it.Close()
	r := 0
	for it.Next() {
		r++
		cs, err := it.Columns()
		if err != nil {
			return 0, 0, err
		}
		last := 0
		for i, c := range cs {
			if c != "" {
				last = i + 1
			}
		}
		if last > 0 {
			rows = r
			cols = max(cols, last)
		}
	}
	return rows, cols, it.Error()
}

// writeSheetsCSV 把每个非空工作表写成一个 CSV。outFor 根据工作表名和「是否有多个」决定输出路径。
func writeSheetsCSV(t *task, f *excelize.File, outFor func(sheet string, multi bool) string) error {
	type ext struct {
		name       string
		rows, cols int
	}
	var list []ext
	for _, s := range f.GetSheetList() {
		r, c, err := sheetExtent(f, s)
		if err != nil || r == 0 {
			continue // 图表工作表或者空表
		}
		list = append(list, ext{s, r, c})
	}
	if len(list) == 0 {
		return conv.Fail("表格是空的，没有可以导出的内容", "")
	}
	for _, s := range list {
		if t.cancelled() {
			return conv.ErrCancelled
		}
		t.report(-1, "正在导出工作表「"+s.name+"」…")
		out := outFor(s.name, len(list) > 1)
		cw, err := newCSVWriter(out)
		if err != nil {
			return err
		}
		it, err := f.Rows(s.name)
		if err != nil {
			cw.close()
			return conv.Fail("没法读取工作表「"+s.name+"」", err.Error())
		}
		r := 0
		for it.Next() && r < s.rows {
			r++
			cs, err := it.Columns()
			if err != nil {
				it.Close()
				cw.close()
				return conv.Fail("没法读取工作表「"+s.name+"」", err.Error())
			}
			rec := make([]string, s.cols)
			copy(rec, cs)
			cw.w.Write(rec)
		}
		it.Close()
		if err := cw.close(); err != nil {
			return err
		}
	}
	return nil
}
