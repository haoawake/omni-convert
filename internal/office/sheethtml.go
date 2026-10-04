package office

import (
	"bufio"
	"fmt"
	"html"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 表格 → 网页：每个工作表一个表格，保留合并单元格、粗体斜体、文字颜色、填充色、对齐方式和列宽。
// 纯 Go 完成，生成的是一个独立的 .html 文件（Excel 自己存的网页会带一个文件夹和框架页）。

const sheetCSS = `
:root { color-scheme: light; }
html { -webkit-print-color-adjust: exact; print-color-adjust: exact; }
body { margin: 24px; background: #fff; color: #1f2328;
  font: 13px/1.45 "Segoe UI", "Microsoft YaHei", "PingFang SC", "Noto Sans CJK SC", sans-serif; }
h1 { font-size: 20px; margin: 0 0 16px; }
h2 { font-size: 16px; margin: 28px 0 10px; padding-bottom: 4px; border-bottom: 1px solid #d0d7de; }
.sheet { overflow-x: auto; margin-bottom: 8px; }
table { border-collapse: collapse; table-layout: auto; }
td { border: 1px solid #d0d7de; padding: 3px 8px; vertical-align: bottom; white-space: pre-wrap; }
td.n { text-align: right; font-variant-numeric: tabular-nums; }
tr:first-child td { background: #f6f8fa; }
@media print { body { margin: 0; } .sheet { overflow: visible; } }
`

// sheetsToHTML 把整个工作簿写成一个网页
func sheetsToHTML(t *task, f *excelize.File, out, title string) error {
	type sheetInfo struct {
		name       string
		rows, cols int
	}
	var sheets []sheetInfo
	for _, s := range f.GetSheetList() {
		if vis, err := f.GetSheetVisible(s); err == nil && !vis {
			continue
		}
		r, c, err := sheetExtent(f, s)
		if err != nil || r == 0 {
			continue
		}
		sheets = append(sheets, sheetInfo{s, r, c})
	}
	if len(sheets) == 0 {
		return conv.Fail("表格是空的，没有可以导出的内容", "")
	}
	fo, err := os.Create(out)
	if err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	w := bufio.NewWriterSize(fo, 256*1024)
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>%s</title>\n<style>%s</style>\n</head>\n<body>\n<h1>%s</h1>\n",
		html.EscapeString(title), sheetCSS, html.EscapeString(title))
	for _, s := range sheets {
		if t.cancelled() {
			fo.Close()
			return conv.ErrCancelled
		}
		t.report(-1, "正在导出工作表「"+s.name+"」…")
		if len(sheets) > 1 {
			fmt.Fprintf(w, "<h2>%s</h2>\n", html.EscapeString(s.name))
		}
		if err := writeSheetTable(w, f, s.name, s.rows, s.cols); err != nil {
			fo.Close()
			return conv.Fail("没法读取工作表「"+s.name+"」", err.Error())
		}
	}
	w.WriteString("</body>\n</html>\n")
	if err := w.Flush(); err != nil {
		fo.Close()
		return conv.Fail("没法写入输出文件", err.Error())
	}
	if err := fo.Close(); err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	return nil
}

type span struct{ rows, cols int }

func writeSheetTable(w *bufio.Writer, f *excelize.File, sheet string, nRows, nCols int) error {
	// 合并单元格：左上角记跨度，其余格子跳过
	spans := map[[2]int]span{}
	covered := map[[2]int]bool{}
	if mcs, err := f.GetMergeCells(sheet, true); err == nil {
		for _, mc := range mcs {
			c1, r1, e1 := excelize.CellNameToCoordinates(mc.GetStartAxis())
			c2, r2, e2 := excelize.CellNameToCoordinates(mc.GetEndAxis())
			if e1 != nil || e2 != nil {
				continue
			}
			nRows, nCols = max(nRows, r2), max(nCols, c2)
			spans[[2]int{r1, c1}] = span{r2 - r1 + 1, c2 - c1 + 1}
			for r := r1; r <= r2; r++ {
				for c := c1; c <= c2; c++ {
					if r != r1 || c != c1 {
						covered[[2]int{r, c}] = true
					}
				}
			}
		}
	}
	styled := nRows*nCols <= 300000 // 特别大的表不逐格读样式，太慢
	css := map[int]string{}
	w.WriteString("<div class=\"sheet\"><table>\n<colgroup>")
	for c := 1; c <= nCols; c++ {
		name, _ := excelize.ColumnNumberToName(c)
		if cw, err := f.GetColWidth(sheet, name); err == nil && cw > 0 {
			fmt.Fprintf(w, "<col style=\"min-width:%dpx\">", int(cw*7+5))
		} else {
			w.WriteString("<col>")
		}
	}
	w.WriteString("</colgroup>\n")
	it, err := f.Rows(sheet)
	if err != nil {
		return err
	}
	defer it.Close()
	for r := 1; r <= nRows; r++ {
		var cells []string
		if it.Next() {
			if cells, err = it.Columns(); err != nil {
				return err
			}
		}
		w.WriteString("<tr>")
		for c := 1; c <= nCols; c++ {
			if covered[[2]int{r, c}] {
				continue
			}
			val := ""
			if c <= len(cells) {
				val = cells[c-1]
			}
			w.WriteString("<td")
			if sp, ok := spans[[2]int{r, c}]; ok {
				if sp.rows > 1 {
					fmt.Fprintf(w, " rowspan=\"%d\"", sp.rows)
				}
				if sp.cols > 1 {
					fmt.Fprintf(w, " colspan=\"%d\"", sp.cols)
				}
			}
			if styled {
				axis, _ := excelize.CoordinatesToCellName(c, r)
				if val != "" {
					if ct, err := f.GetCellType(sheet, axis); err == nil && (ct == excelize.CellTypeNumber || ct == excelize.CellTypeDate || (ct == excelize.CellTypeUnset || ct == excelize.CellTypeFormula) && looksNumeric(val)) {
						w.WriteString(" class=\"n\"")
					}
				}
				if id, err := f.GetCellStyle(sheet, axis); err == nil && id > 0 {
					st, ok := css[id]
					if !ok {
						st = styleCSS(f, id)
						css[id] = st
					}
					if st != "" {
						fmt.Fprintf(w, " style=\"%s\"", st)
					}
				}
			}
			w.WriteString(">")
			w.WriteString(html.EscapeString(val))
			w.WriteString("</td>")
		}
		w.WriteString("</tr>\n")
	}
	w.WriteString("</table></div>\n")
	return nil
}

func looksNumeric(s string) bool {
	return reNumber.MatchString(s) || reThousands.MatchString(s) || rePercent.MatchString(s)
}

// styleCSS 把 Excel 单元格样式翻译成 CSS
func styleCSS(f *excelize.File, id int) string {
	st, err := f.GetStyle(id)
	if err != nil || st == nil {
		return ""
	}
	var b []string
	if ft := st.Font; ft != nil {
		if ft.Bold {
			b = append(b, "font-weight:bold")
		}
		if ft.Italic {
			b = append(b, "font-style:italic")
		}
		if ft.Underline != "" && ft.Underline != "none" {
			b = append(b, "text-decoration:underline")
		}
		if ft.Strike {
			b = append(b, "text-decoration:line-through")
		}
		if c := cssColor(ft.Color); c != "" && c != "#000000" {
			b = append(b, "color:"+c)
		}
		if ft.Size > 0 && ft.Size != 11 {
			b = append(b, fmt.Sprintf("font-size:%.1fpt", ft.Size))
		}
	}
	if fl := st.Fill; fl.Type == "pattern" && fl.Pattern == 1 && len(fl.Color) > 0 {
		if c := cssColor(fl.Color[0]); c != "" {
			b = append(b, "background:"+c)
		}
	}
	if al := st.Alignment; al != nil {
		switch al.Horizontal {
		case "center", "centerContinuous", "distributed":
			b = append(b, "text-align:center")
		case "right":
			b = append(b, "text-align:right")
		case "left":
			b = append(b, "text-align:left")
		case "justify":
			b = append(b, "text-align:justify")
		}
		switch al.Vertical {
		case "top":
			b = append(b, "vertical-align:top")
		case "center":
			b = append(b, "vertical-align:middle")
		}
	}
	return strings.Join(b, ";")
}

// cssColor 把 Excel 的颜色（RRGGBB、#RRGGBB、AARRGGBB）变成 #rrggbb
func cssColor(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 8 {
		s = s[2:]
	}
	if len(s) != 6 {
		return ""
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return ""
		}
	}
	return "#" + strings.ToLower(s)
}
