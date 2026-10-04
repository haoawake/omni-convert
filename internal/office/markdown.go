package office

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Markdown → 网页：GFM（表格、删除线、任务列表、自动链接）+ 脚注 + 中文换行处理，套一个干净的样式。

// 网页里的本地图片怎么处理
type imgMode int

const (
	imgEmbed   imgMode = iota // 嵌进网页（data: 地址），生成的网页单独拿走也能看
	imgFileURL                // 换成绝对的 file:/// 地址（给 Edge 用，图片不用复制）
	imgCopy                   // 复制到网页旁边、换成英文文件名（给 Word 用：它不认带中文转义的 file 地址）
)

const fontStack = `-apple-system, "Segoe UI", "Microsoft YaHei", "PingFang SC", "Hiragino Sans GB", "Noto Sans CJK SC", "Helvetica Neue", Arial, sans-serif`
const monoStack = `"Cascadia Mono", Consolas, "Microsoft YaHei Mono", "Courier New", "Microsoft YaHei", monospace`

const markdownCSS = `
:root { color-scheme: light; }
html { -webkit-text-size-adjust: 100%; -webkit-print-color-adjust: exact; print-color-adjust: exact; }
body { margin: 0; background: #fff; color: #1f2328; font-family: ` + fontStack + `; font-size: 16px; line-height: 1.7; }
.markdown-body { max-width: 860px; margin: 0 auto; padding: 40px 32px 64px; overflow-wrap: break-word; }
.markdown-body > :first-child { margin-top: 0; }
h1, h2, h3, h4, h5, h6 { margin: 1.5em 0 .6em; font-weight: 600; line-height: 1.3; }
h1 { font-size: 2em; padding-bottom: .3em; border-bottom: 1px solid #d8dee4; }
h2 { font-size: 1.5em; padding-bottom: .3em; border-bottom: 1px solid #d8dee4; }
h3 { font-size: 1.25em; } h4 { font-size: 1em; } h5 { font-size: .875em; } h6 { font-size: .85em; color: #59636e; }
p, ul, ol, dl, blockquote, pre, table, details { margin: 0 0 1em; }
ul, ol { padding-left: 2em; } li + li { margin-top: .25em; }
li > input[type=checkbox] { margin: 0 .45em 0 -1.3em; vertical-align: middle; }
a { color: #0969da; text-decoration: none; } a:hover { text-decoration: underline; }
strong { font-weight: 600; } del { color: #59636e; }
code, kbd, samp { font-family: ` + monoStack + `; font-size: .875em; }
code { background: rgba(175, 184, 193, .2); padding: .15em .4em; border-radius: 6px; }
pre { background: #f6f8fa; padding: 14px 16px; border-radius: 6px; overflow: auto; line-height: 1.5; }
pre code { background: none; padding: 0; font-size: .85em; white-space: pre; border-radius: 0; }
blockquote { margin-left: 0; padding: 0 1em; color: #59636e; border-left: .25em solid #d1d9e0; }
table { border-collapse: collapse; border-spacing: 0; display: block; width: max-content; max-width: 100%; overflow: auto; }
th, td { border: 1px solid #d1d9e0; padding: 6px 13px; }
th { font-weight: 600; background: #f6f8fa; }
tr:nth-child(2n) td { background: #f6f8fa; }
img { max-width: 100%; height: auto; }
hr { height: .25em; padding: 0; margin: 24px 0; background: #d1d9e0; border: 0; }
.footnotes { font-size: .875em; color: #59636e; }
@page { size: A4; margin: 16mm 15mm; }
@media print {
  body { font-size: 11pt; }
  .markdown-body { max-width: none; padding: 0; }
  pre, pre code { white-space: pre-wrap; word-break: break-all; }
  pre, blockquote, tr, img { break-inside: avoid; }
  h1, h2, h3, h4 { break-after: avoid; }
  table { display: table; width: auto; overflow: visible; }
}
`

// Word 对 CSS 的支持有限，给它一份简单直接的样式
const markdownWordCSS = `
body { font-family: "Microsoft YaHei", "微软雅黑", sans-serif; font-size: 10.5pt; line-height: 150%; color: #1f2328; }
h1 { font-size: 20pt; } h2 { font-size: 16pt; } h3 { font-size: 13pt; } h4, h5, h6 { font-size: 11pt; }
h1, h2, h3, h4, h5, h6 { font-family: "Microsoft YaHei", "微软雅黑", sans-serif; color: #1f2328; }
p { margin: 0 0 6pt 0; }
a { color: #0969da; }
code { font-family: Consolas, "Microsoft YaHei"; font-size: 9.5pt; background: #eff1f3; }
pre { font-family: Consolas, "Microsoft YaHei"; font-size: 9pt; background: #f6f8fa; border: 1px solid #d1d9e0; padding: 6pt; line-height: 130%; }
blockquote { margin-left: 0; padding-left: 8pt; border-left: 3pt solid #d1d9e0; color: #59636e; }
table { border-collapse: collapse; margin-bottom: 8pt; }
th, td { border: 1px solid #a6a6a6; padding: 2pt 6pt; line-height: 120%; }
th { background: #f2f2f2; font-weight: bold; }
ul, ol { margin-top: 0; margin-bottom: 6pt; }
img { margin: 4pt 0; }
`

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.CJK, extension.Footnote),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()), // 允许 Markdown 里夹带的 HTML（很多 README 用到）
)

// renderMarkdown 把 Markdown 文件变成完整的网页
// mode 是 imgCopy 时图片复制到 copyDir（生成的网页也要放在那里）。
func renderMarkdown(path string, mode imgMode, forWord bool, copyDir string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src, _ := decodeText(raw)
	src = strings.TrimPrefix(src, "\uFEFF")
	body, fmTitle := stripFrontMatter(src)
	source := []byte(body)
	doc := md.Parser().Parse(text.NewReader(source))
	title := firstHeading(doc, source)
	if title == "" {
		title = fmTitle
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, source, doc); err != nil {
		return nil, err
	}
	content := rewriteImages(buf.Bytes(), filepath.Dir(path), mode, copyDir)
	css := markdownCSS
	if forWord {
		css = markdownWordCSS
	}
	return wrapHTML(title, css, `<article class="markdown-body">`+"\n"+string(content)+"</article>"), nil
}

func wrapHTML(title, css, body string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(title) + "</title>\n<style>" + css + "</style>\n</head>\n<body>\n")
	b.WriteString(body)
	b.WriteString("\n</body>\n</html>\n")
	return []byte(b.String())
}

var reFrontKey = regexp.MustCompile(`^[A-Za-z0-9_-]+\s*:`)

// stripFrontMatter 去掉开头的 YAML 元数据（Hexo、Jekyll、Obsidian 常用），顺便取出 title
func stripFrontMatter(s string) (body, title string) {
	norm := strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return s, ""
	}
	lines := strings.Split(norm, "\n")
	for i := 1; i < len(lines) && i < 300; i++ {
		l := strings.TrimSpace(lines[i])
		if l == "---" || l == "..." {
			meta := lines[1:i]
			keys := 0
			for _, m := range meta {
				if reFrontKey.MatchString(m) {
					keys++
					if k, v, ok := strings.Cut(m, ":"); ok && strings.TrimSpace(k) == "title" {
						title = strings.Trim(strings.TrimSpace(v), `"'`)
					}
				}
			}
			if keys == 0 && len(meta) > 0 {
				return s, ""
			}
			return strings.Join(lines[i+1:], "\n"), title
		}
	}
	return s, ""
}

// firstHeading 取第一个一级标题的文字做网页标题
func firstHeading(doc ast.Node, source []byte) string {
	var title string
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering && h.Level == 1 {
			title = strings.TrimSpace(nodeText(h, source))
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return title
}

func nodeText(n ast.Node, source []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
			if t.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		default:
			b.WriteString(nodeText(c, source))
		}
	}
	return b.String()
}

var reImgSrc = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc\s*=\s*)(?:"([^"]*)"|'([^']*)'|([^\s>"']+))`)

// rewriteImages 处理网页里引用的本地图片：嵌进网页、换成绝对地址或者复制到 copyDir
func rewriteImages(page []byte, baseDir string, mode imgMode, copyDir string) []byte {
	n := 0
	return reImgSrc.ReplaceAllFunc(page, func(m []byte) []byte {
		sub := reImgSrc.FindSubmatch(m)
		src := string(sub[2]) + string(sub[3]) + string(sub[4])
		p, ok := localFile(html.UnescapeString(src), baseDir)
		if !ok {
			return m
		}
		var repl string
		switch mode {
		case imgEmbed:
			data, ok := dataURI(p)
			if !ok {
				return m
			}
			repl = data
		case imgCopy:
			n++
			name := fmt.Sprintf("img%d%s", n, strings.ToLower(filepath.Ext(p)))
			if copyFile(p, filepath.Join(copyDir, name)) != nil {
				return m
			}
			repl = name
		default:
			repl = fileURL(p)
		}
		return append(append([]byte(nil), sub[1]...), []byte(`"`+repl+`"`)...)
	})
}

// localFile 判断 src 是不是指向一个本地文件，是的话返回完整路径
func localFile(src, baseDir string) (string, bool) {
	src = strings.TrimSpace(src)
	if src == "" || strings.HasPrefix(src, "#") || strings.HasPrefix(src, "//") {
		return "", false
	}
	if u, err := url.Parse(src); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		if !strings.EqualFold(u.Scheme, "file") {
			return "", false // http、https、data 之类的不管
		}
		p := u.Path
		if u.Host != "" {
			p = `\\` + u.Host + p
		} else {
			p = strings.TrimPrefix(p, "/")
		}
		p = filepath.FromSlash(p)
		return p, fileExists(p)
	}
	if i := strings.IndexAny(src, "?#"); i >= 0 {
		src = src[:i]
	}
	if s, err := url.PathUnescape(src); err == nil {
		src = s
	}
	p := filepath.FromSlash(src)
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	return p, fileExists(p)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// dataURI 把文件变成 data: 地址（超过 20MB 的不嵌）
func dataURI(p string) (string, bool) {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() > 20<<20 {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
	switch strings.ToLower(filepath.Ext(p)) {
	case ".svg":
		typ = "image/svg+xml"
	case ".webp":
		typ = "image/webp"
	case ".jpg", ".jpeg":
		typ = "image/jpeg"
	case ".png":
		typ = "image/png"
	case ".gif":
		typ = "image/gif"
	case ".bmp":
		typ = "image/bmp"
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	if i := strings.Index(typ, ";"); i >= 0 {
		typ = typ[:i]
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(b), true
}

// fileURL 把本地路径变成 file:/// 地址（中文、空格会被转义）
func fileURL(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	s := filepath.ToSlash(p)
	u := url.URL{Scheme: "file"}
	if strings.HasPrefix(s, "//") { // \\server\share
		rest := strings.TrimPrefix(s, "//")
		host, path, _ := strings.Cut(rest, "/")
		u.Host, u.Path = host, "/"+path
	} else {
		if !strings.HasPrefix(s, "/") {
			s = "/" + s
		}
		u.Path = s
	}
	return u.String()
}
