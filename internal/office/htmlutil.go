package office

import (
	"bytes"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 网页相关的小工具。

const textCSS = `
:root { color-scheme: light; }
html { -webkit-print-color-adjust: exact; print-color-adjust: exact; }
body { margin: 0; background: #fff; color: #1f2328; }
pre.txt { max-width: 900px; margin: 0 auto; padding: 32px; white-space: pre-wrap; overflow-wrap: anywhere;
  font-family: ` + fontStack + `; font-size: 15px; line-height: 1.75; tab-size: 4; }
@page { size: A4; margin: 16mm 15mm; }
@media print { pre.txt { max-width: none; padding: 0; font-size: 10.5pt; } }
`

// textHTML 把纯文本变成网页（保留换行和空格，长行自动折行）
func textHTML(title, text string) []byte {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\uFEFF")
	return wrapHTML(title, textCSS, `<pre class="txt">`+html.EscapeString(text)+`</pre>`)
}

var (
	reBaseTag     = regexp.MustCompile(`(?i)<base\b`)
	reMetaCharset = regexp.MustCompile(`(?i)<meta\b[^>]*charset[^>]*>`)
	reHeadTag     = regexp.MustCompile(`(?i)<head\b[^>]*>`)
	reHTMLTag     = regexp.MustCompile(`(?i)<html\b[^>]*>`)
	reFileList    = regexp.MustCompile(`(?i)<link\b[^>]*rel\s*=\s*["']?(File-List|Edit-Time-Data|themeData|colorSchemeMapping|OLE-Object-Data)["']?[^>]*>\s*`)
)

// printableHTML 准备一份给 Edge 打印的网页副本：纸张设为 A4，并用 <base> 让相对路径的图片、样式仍然指向原来的位置。
// 原文件不动。mht 和 UTF-16 的网页直接用原文件。
func (t *task) printableHTML(in string) (string, error) {
	ext := conv.Ext(in)
	if ext == ".mht" || ext == ".mhtml" {
		return in, nil
	}
	b, err := os.ReadFile(in)
	if err != nil {
		return "", conv.Fail("没法读取文件", err.Error())
	}
	if e, ok := sniffBOM(b); ok && e != encUTF8BOM {
		return in, nil
	}
	snippet := `<style>@page { size: A4; margin: 12mm; }</style>`
	if !reBaseTag.Match(b) {
		snippet = `<base href="` + html.EscapeString(fileURL(filepath.Dir(in))) + `/"/>` + snippet
	}
	pos := 0
	head := b[:min(len(b), 4096)]
	if loc := reMetaCharset.FindIndex(head); loc != nil { // 放在编码声明后面，免得把它挤出前 1024 字节
		pos = loc[1]
	} else if loc := reHeadTag.FindIndex(b); loc != nil {
		pos = loc[1]
	} else if loc := reHTMLTag.FindIndex(b); loc != nil {
		pos = loc[1]
	} else if bytes.HasPrefix(b, bomUTF8) {
		pos = len(bomUTF8)
	}
	out := make([]byte, 0, len(b)+len(snippet))
	out = append(out, b[:pos]...)
	out = append(out, snippet...)
	out = append(out, b[pos:]...)
	if ext != ".xhtml" {
		ext = ".html"
	}
	p, err := t.tempPath("page", ext)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// inlineHTMLFile 把 Word / LibreOffice 另存的网页里引用的图片嵌进网页本身，写成一个独立的文件
func inlineHTMLFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	b = toUTF8HTML(b)
	b = rewriteImages(b, filepath.Dir(src), imgEmbed, "")
	b = reFileList.ReplaceAll(b, nil)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	return nil
}

var reCharsetValue = regexp.MustCompile(`(?i)(<meta\b[^>]*charset\s*=\s*["']?)[\w-]+`)

// toUTF8HTML 把 GBK 等编码的网页转成 UTF-8，并改掉里面的编码声明
func toUTF8HTML(b []byte) []byte {
	e, name, _ := charset.DetermineEncoding(b, "text/html")
	if name == "utf-8" || e == nil {
		return b
	}
	dec, err := e.NewDecoder().Bytes(b)
	if err != nil {
		return b
	}
	if reCharsetValue.Match(dec) {
		return reCharsetValue.ReplaceAll(dec, []byte("${1}utf-8"))
	}
	return append([]byte(`<meta charset="utf-8">`+"\n"), dec...)
}

// htmlToText 不用 Office 时的网页转文本：按块元素分行、表格单元格用制表符分隔
func htmlToText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r, err := charset.NewReader(f, "text/html")
	if err != nil {
		return "", err
	}
	doc, err := xhtml.Parse(r)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	newline := func() {
		s := b.String()
		if s != "" && !strings.HasSuffix(s, "\n") {
			b.WriteString("\n")
		}
	}
	var walk func(n *xhtml.Node, pre bool)
	walk = func(n *xhtml.Node, pre bool) {
		switch n.Type {
		case xhtml.TextNode:
			s := n.Data
			if !pre {
				s = strings.Join(strings.Fields(s), " ")
				if s == "" {
					return
				}
				if cur := b.String(); cur != "" && !strings.HasSuffix(cur, "\n") && !strings.HasSuffix(cur, "\t") && !strings.HasSuffix(cur, " ") &&
					(strings.HasPrefix(n.Data, " ") || strings.HasPrefix(n.Data, "\n")) {
					b.WriteString(" ")
				}
			}
			b.WriteString(s)
			return
		case xhtml.ElementNode:
			switch n.Data {
			case "script", "style", "head", "noscript", "template", "svg":
				return
			case "br":
				b.WriteString("\n")
				return
			case "pre", "textarea":
				pre = true
			case "td", "th":
				if n.PrevSibling != nil {
					b.WriteString("\t")
				}
			case "li":
				newline()
				b.WriteString("• ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, pre)
		}
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "table", "ul", "ol", "li", "pre",
				"blockquote", "section", "article", "header", "footer", "hr", "dl", "dt", "dd", "figure", "main", "nav", "aside", "address":
				newline()
			}
			switch n.Data {
			case "p", "h1", "h2", "h3", "h4", "h5", "h6", "table", "pre", "blockquote":
				b.WriteString("\n")
			}
		}
	}
	walk(doc, false)
	// 合并多余的空行
	lines := strings.Split(b.String(), "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n", nil
}
