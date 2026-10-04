package pdf

// PDF 提取文字：转 TXT，以及没装 Word 时的「PDF 转 Word（纯文字）」

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/haoawake/omni-convert/internal/conv"
)

var errNoText = conv.Fail("这个 PDF 里没有可以提取的文字，可能是扫描出来的图片", "需要文字识别（OCR）才能提取")

// pageTexts 取出选中各页的文字
func pageTexts(ctx context.Context, j *conv.Job) (pages []int, texts []string, landscape bool, err error) {
	d, err := openForJob(j, j.Input())
	if err != nil {
		return nil, nil, false, err
	}
	defer d.Close()
	pages, err = selectedPages(j.Opt, d.PageCount())
	if err != nil {
		return nil, nil, false, err
	}
	w, h := d.PageSize(pages[0])
	landscape = w > h*1.05
	any := false
	for k, p := range pages {
		if err := cancelled(ctx); err != nil {
			return nil, nil, false, err
		}
		j.Report(float64(k)/float64(len(pages)), fmt.Sprintf("第 %d/%d 页", k+1, len(pages)))
		t := d.Text(p)
		if strings.TrimSpace(t) != "" {
			any = true
		}
		texts = append(texts, t)
	}
	if !any {
		return nil, nil, false, errNoText
	}
	return pages, texts, landscape, nil
}

// ToText 提取 PDF 里的文字，存成 UTF-8（带 BOM）、Windows 换行的 .txt，页与页之间有「—— 第 N 页 ——」
func ToText() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		pages, texts, _, err := pageTexts(ctx, j)
		if err != nil {
			return err
		}
		var b strings.Builder
		b.WriteString("\xEF\xBB\xBF") // BOM，让记事本认出是 UTF-8
		for k, t := range texts {
			if k > 0 {
				fmt.Fprintf(&b, "\r\n\r\n—— 第 %d 页 ——\r\n\r\n", pages[k]+1)
			}
			b.WriteString(strings.ReplaceAll(t, "\n", "\r\n"))
		}
		b.WriteString("\r\n")
		out := j.OutFile(".txt")
		return writeFile(out, func(w io.Writer) error {
			_, err := io.WriteString(w, b.String())
			return err
		})
	})
}

// ---------------------------------------------------------------- 拼段落

// 句末标点：以它们结尾的行不和下一行拼起来
const sentenceEnd = "。！？；：…」』”’）)】》.!?:;"

// 列表、编号开头的行自成一段
var listStart = regexp.MustCompile(`^\s*([•·●○■□◆◇▪▫►✓√\-–—*]|\d{1,3}[.、)）]|[（(]\d{1,3}[)）]|[一二三四五六七八九十]{1,3}、|[（(][一二三四五六七八九十]{1,3}[)）]|第[一二三四五六七八九十百\d]+[章节条部分])`)

// textWidth 估计一行的显示宽度：中日韩字符算 2，其它算 1
func textWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func isWide(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// paragraphs 把一页的文字拆成段落：空行分段；明显是因为排版自动换行而断开的行拼回去
func paragraphs(text string) []string {
	lines := strings.Split(text, "\n")
	// 「满行」的宽度：取最长的几行的宽度
	widths := make([]int, 0, len(lines))
	for _, l := range lines {
		if w := textWidth(strings.TrimSpace(l)); w > 0 {
			widths = append(widths, w)
		}
	}
	full := 0
	if len(widths) > 0 {
		sort.Ints(widths)
		full = widths[len(widths)*9/10]
	}

	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i, raw := range lines {
		l := strings.TrimRightFunc(raw, unicode.IsSpace)
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		if cur.Len() > 0 {
			prev := cur.String()
			l = strings.TrimLeftFunc(l, unicode.IsSpace)
			if strings.HasSuffix(prev, "-") && hyphenJoin(prev, l) {
				// 被连字符断开的英文单词，去掉连字符直接拼
				cur.Reset()
				cur.WriteString(strings.TrimSuffix(prev, "-"))
			} else {
				cur.WriteString(joiner(prev, l))
			}
		}
		cur.WriteString(l)
		// 决定要不要和下一行拼起来
		next := ""
		if i+1 < len(lines) {
			next = strings.TrimSpace(lines[i+1])
		}
		if next == "" || !wrapped(l, next, full) {
			flush()
		}
	}
	flush()
	return out
}

// wrapped 判断 line 是不是因为排满了才换到 next 的
func wrapped(line, next string, full int) bool {
	t := strings.TrimSpace(line)
	if full < 20 || textWidth(t) < full*8/10 {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(t)
	if strings.ContainsRune(sentenceEnd, last) {
		return false
	}
	return !listStart.MatchString(next)
}

// joiner 是把两行拼起来时中间要不要加空格：中文之间不加，英文单词之间加
func joiner(prev, next string) string {
	a, _ := utf8.DecodeLastRuneInString(prev)
	b, _ := utf8.DecodeRuneInString(next)
	if isWide(a) || isWide(b) || a == '-' {
		return ""
	}
	return " "
}

// hyphenJoin 判断行尾的连字符是不是把一个英文单词断开了（比如 "conver-" + "sion"）
func hyphenJoin(prev, next string) bool {
	r := []rune(strings.TrimSuffix(prev, "-"))
	if len(r) == 0 || !unicode.IsLetter(r[len(r)-1]) || isWide(r[len(r)-1]) {
		return false
	}
	b, _ := utf8.DecodeRuneInString(next)
	return unicode.IsLower(b)
}

// ---------------------------------------------------------------- docx

// ToDocxText 没装 Word 时的 PDF 转 Word：每页的文字变成段落，页与页之间分页。只有文字，没有图片和排版。
func ToDocxText() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		_, texts, landscape, err := pageTexts(ctx, j)
		if err != nil {
			return err
		}
		var body strings.Builder
		for k, t := range texts {
			paras := paragraphs(t)
			if len(paras) == 0 {
				paras = []string{""}
			}
			for i, p := range paras {
				body.WriteString("<w:p>")
				if k > 0 && i == 0 {
					body.WriteString(`<w:pPr><w:pageBreakBefore/></w:pPr>`)
				}
				writeRuns(&body, p)
				body.WriteString("</w:p>")
			}
		}
		pg := `<w:pgSz w:w="11906" w:h="16838"/>`
		if landscape {
			pg = `<w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/>`
		}
		doc := xmlHead + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` +
			body.String() +
			`<w:sectPr>` + pg + `<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="851" w:footer="992" w:gutter="0"/></w:sectPr></w:body></w:document>`

		out := j.OutFile(".docx")
		return writeFile(out, func(w io.Writer) error {
			z := zip.NewWriter(w)
			files := []struct{ name, data string }{
				{"[Content_Types].xml", docxContentTypes},
				{"_rels/.rels", docxRels},
				{"word/document.xml", doc},
				{"word/_rels/document.xml.rels", docxDocRels},
				{"word/styles.xml", docxStyles},
				{"docProps/core.xml", coreXML(j.Base())},
				{"docProps/app.xml", appXML("")},
			}
			for _, f := range files {
				if err := zipText(z, f.name, f.data); err != nil {
					return err
				}
			}
			return z.Close()
		})
	})
}

// writeRuns 写一段文字，制表符变成 <w:tab/>
func writeRuns(b *strings.Builder, text string) {
	if text == "" {
		return
	}
	b.WriteString("<w:r>")
	for i, part := range strings.Split(text, "\t") {
		if i > 0 {
			b.WriteString("<w:tab/>")
		}
		if part != "" {
			b.WriteString(`<w:t xml:space="preserve">`)
			b.WriteString(xmlEscape(part))
			b.WriteString("</w:t>")
		}
	}
	b.WriteString("</w:r>")
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// xmlEscape 转义 XML 特殊字符，并去掉 XML 里不允许出现的字符
func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || r >= 0x10000:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func zipText(z *zip.Writer, name, data string) error {
	w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, data)
	return err
}

func coreXML(title string) string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return xmlHead + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + xmlEscape(title) + `</dc:title><dc:creator>万能格式转换</dc:creator>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified></cp:coreProperties>`
}

func appXML(extra string) string {
	return xmlHead + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>万能格式转换</Application>` + extra + `</Properties>`
}

const docxContentTypes = xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
	`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
	`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
	`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
	`</Types>`

const docxRels = xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
	`</Relationships>`

const docxDocRels = xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`</Relationships>`

// 默认字体：英文 Calibri，中文宋体，五号字（10.5 磅），段后 6 磅，1.15 倍行距
const docxStyles = xmlHead + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
	`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:eastAsia="宋体" w:hAnsi="Calibri" w:cs="Times New Roman"/>` +
	`<w:sz w:val="21"/><w:szCs w:val="21"/><w:lang w:val="en-US" w:eastAsia="zh-CN" w:bidi="ar-SA"/></w:rPr></w:rPrDefault>` +
	`<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/><w:pPr><w:widowControl w:val="0"/><w:jc w:val="both"/></w:pPr></w:style>` +
	`</w:styles>`
