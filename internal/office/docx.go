package office

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/haoawake/omni-convert/internal/conv"
)

// PDF → Excel 的后半段：从 Word 重排出来的 docx 里取出表格（和正文段落），写进 xlsx。

const nsW = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

type docxCell struct {
	text   string
	span   int // 横向合并了几列
	vmerge int // 0 不合并，1 纵向合并的开头，2 被上面的格子合并
}

type docxTable [][]docxCell

// readDocx 读出 docx 里的顶层表格和表格外的段落
func readDocx(path string) (tables []docxTable, paras []string, err error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()
	var f *zip.File
	for _, zf := range zr.File {
		if zf.Name == "word/document.xml" {
			f = zf
		}
	}
	if f == nil {
		return nil, nil, fmt.Errorf("docx 里没有 word/document.xml")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()
	return parseDocumentXML(rc)
}

func parseDocumentXML(r io.Reader) (tables []docxTable, paras []string, err error) {
	dec := xml.NewDecoder(r)
	var (
		depth    int // 表格嵌套层数
		cur      docxTable
		row      []docxCell
		cell     *docxCell
		cellText []string
		para     strings.Builder
		inPara   bool
		inText   bool
		skip     int // 在 mc:Fallback 里（和 mc:Choice 内容重复）
		gridSkip int // 行首空出的列（w:gridBefore）
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if e.Name.Local == "Fallback" {
				skip++
			}
			if skip > 0 {
				continue
			}
			if e.Name.Space != nsW {
				continue
			}
			switch e.Name.Local {
			case "tbl":
				depth++
				if depth == 1 {
					cur = nil
				}
			case "tr":
				if depth == 1 {
					row = nil
					gridSkip = 0
				}
			case "gridBefore":
				if depth == 1 {
					gridSkip = attrInt(e, "val", 0)
				}
			case "tc":
				if depth == 1 {
					for ; gridSkip > 0; gridSkip-- {
						row = append(row, docxCell{span: 1})
					}
					cell = &docxCell{span: 1}
					cellText = nil
				}
			case "gridSpan":
				if depth == 1 && cell != nil {
					cell.span = max(1, attrInt(e, "val", 1))
				}
			case "vMerge":
				if depth == 1 && cell != nil {
					if attrStr(e, "val") == "restart" {
						cell.vmerge = 1
					} else {
						cell.vmerge = 2
					}
				}
			case "p":
				inPara = true
				para.Reset()
			case "t":
				inText = true
			case "tab":
				if inPara {
					para.WriteString("\t")
				}
			case "br", "cr":
				if inPara {
					para.WriteString("\n")
				}
			}
		case xml.EndElement:
			if e.Name.Local == "Fallback" {
				skip--
				continue
			}
			if skip > 0 || e.Name.Space != nsW {
				continue
			}
			switch e.Name.Local {
			case "t":
				inText = false
			case "p":
				inPara = false
				s := strings.TrimRight(para.String(), " ")
				if depth >= 1 {
					cellText = append(cellText, s)
				} else if strings.TrimSpace(s) != "" {
					paras = append(paras, strings.TrimSpace(s))
				}
			case "tc":
				if depth == 1 && cell != nil {
					cell.text = strings.TrimSpace(strings.Join(cellText, "\n"))
					row = append(row, *cell)
					cell = nil
				}
			case "tr":
				if depth == 1 {
					cur = append(cur, row)
				}
			case "tbl":
				if depth == 1 && len(cur) > 0 {
					tables = append(tables, cur)
				}
				depth--
			}
		case xml.CharData:
			if inText && inPara && skip == 0 {
				para.Write(e)
			}
		}
	}
	return tables, paras, nil
}

func attrStr(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func attrInt(e xml.StartElement, local string, def int) int {
	v, err := strconv.Atoi(attrStr(e, local))
	if err != nil {
		return def
	}
	return v
}

// tableEmpty 判断表格是不是一个字都没有（Word 重排时偶尔会生成用来排版的空表格）
func tableEmpty(t docxTable) bool {
	for _, r := range t {
		for _, c := range r {
			if c.text != "" {
				return false
			}
		}
	}
	return true
}

// writeTablesXlsx 每个表格一个工作表；没有表格时把段落一行一个放进 A 列。返回写了几个表格。
func writeTablesXlsx(tables []docxTable, paras []string, out string) (int, error) {
	var keep []docxTable
	for _, t := range tables {
		if !tableEmpty(t) {
			keep = append(keep, t)
		}
	}
	f := excelize.NewFile()
	defer f.Close()
	if len(keep) == 0 {
		name := "文字"
		f.SetSheetName("Sheet1", name)
		if len(paras) == 0 {
			return 0, conv.Fail("PDF 里没有识别出文字，可能是扫描件（图片）", "扫描件需要先做文字识别（OCR）")
		}
		st, _ := f.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"}})
		sw, err := f.NewStreamWriter(name)
		if err != nil {
			return 0, err
		}
		sw.SetColWidth(1, 1, 100)
		for i, p := range paras {
			if len([]rune(p)) > maxCellChars {
				p = string([]rune(p)[:maxCellChars])
			}
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			if err := sw.SetRow(cell, []any{excelize.Cell{StyleID: st, Value: p}}); err != nil {
				return 0, err
			}
		}
		if err := sw.Flush(); err != nil {
			return 0, err
		}
		return 0, f.SaveAs(out)
	}
	styles := newStyleCache(f, true)
	for i, t := range keep {
		name := fmt.Sprintf("表格%d", i+1)
		if i == 0 {
			f.SetSheetName("Sheet1", name)
		} else if _, err := f.NewSheet(name); err != nil {
			return 0, err
		}
		if err := writeDocxTable(f, styles, name, t); err != nil {
			return 0, err
		}
	}
	return len(keep), f.SaveAs(out)
}

func writeDocxTable(f *excelize.File, styles *styleCache, sheet string, t docxTable) error {
	type pos struct{ r, c int }
	vstart := map[int]pos{} // 每一列正在进行的纵向合并的起点
	merges := map[pos]pos{} // 起点 → 终点
	widths := map[int]float64{}
	for ri, row := range t {
		r := ri + 1
		c := 1
		for _, cell := range row {
			span := max(1, cell.span)
			switch cell.vmerge {
			case 2:
				if st, ok := vstart[c]; ok {
					end := merges[st]
					merges[st] = pos{r, max(end.c, c+span-1)}
				}
			default:
				if cell.vmerge == 1 {
					vstart[c] = pos{r, c}
				} else {
					delete(vstart, c)
				}
				if span > 1 || cell.vmerge == 1 {
					merges[pos{r, c}] = pos{r, c + span - 1}
				}
				axis, _ := excelize.CoordinatesToCellName(c, r)
				v := inferCell(cell.text)
				if strings.Contains(cell.text, "\n") {
					v = cellValue{kind: cellText, value: cell.text}
				}
				val := styles.cellFor(v, false)
				if cv, ok := val.(excelize.Cell); ok {
					f.SetCellValue(sheet, axis, cv.Value)
					f.SetCellStyle(sheet, axis, axis, cv.StyleID)
				} else {
					f.SetCellValue(sheet, axis, val)
				}
				if span == 1 {
					for _, line := range strings.Split(cell.text, "\n") {
						widths[c] = math.Max(widths[c], displayWidth(line))
					}
				}
			}
			// 被合并的格子也要有边框
			for k := 0; k < span; k++ {
				axis, _ := excelize.CoordinatesToCellName(c+k, r)
				if cell.vmerge == 2 || k > 0 {
					f.SetCellStyle(sheet, axis, axis, styles.get("", false))
				}
			}
			c += span
		}
	}
	for st, end := range merges {
		if end.r == st.r && end.c == st.c {
			continue
		}
		a, _ := excelize.CoordinatesToCellName(st.c, st.r)
		b, _ := excelize.CoordinatesToCellName(end.c, end.r)
		f.MergeCell(sheet, a, b)
	}
	for c, w := range widths {
		name, _ := excelize.ColumnNumberToName(c)
		f.SetColWidth(sheet, name, name, math.Min(colWidth(w), 50))
	}
	return nil
}
