package office

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 看文件内容判断真实格式。WPS 的 wps/et/dps 其实就是 doc/xls/ppt（或者 docx/xlsx/pptx）；
// 网上下载的「xls」常常是网页或 XML。按真实格式换个扩展名交给 Office，能少很多「格式和扩展名不匹配」的问题。

// sniff 返回 ole（老的二进制 Office 格式）、zip（OOXML、ODF）、pdf、rtf、html、xml、""（不认识）
func sniff(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}):
		return "ole"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return "zip"
	case bytes.HasPrefix(head, []byte("%PDF")):
		return "pdf"
	case bytes.HasPrefix(head, []byte(`{\rtf`)):
		return "rtf"
	}
	s := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(head), "\uFEFF")))
	switch {
	case strings.HasPrefix(s, "<?xml"):
		if strings.Contains(s, "<html") {
			return "html"
		}
		return "xml"
	case strings.HasPrefix(s, "<!doctype html"), strings.HasPrefix(s, "<html"), strings.Contains(s, "<table"),
		strings.HasPrefix(s, "mime-version"):
		return "html"
	}
	return ""
}

// zipKind 根据 [Content_Types].xml 判断 OOXML 文件的真实扩展名（.docx .docm .xlsx .xlsm .xlsb .pptx .pptm），认不出时返回 ""
func zipKind(path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "[Content_Types].xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		s := string(b)
		switch {
		case strings.Contains(s, "sheet.binary.macroEnabled"):
			return ".xlsb"
		case strings.Contains(s, "sheet.macroEnabled"):
			return ".xlsm"
		case strings.Contains(s, "spreadsheetml"):
			return ".xlsx"
		case strings.Contains(s, "document.macroEnabled"):
			return ".docm"
		case strings.Contains(s, "wordprocessingml"):
			return ".docx"
		case strings.Contains(s, "presentation.macroEnabled"):
			return ".pptm"
		case strings.Contains(s, "presentationml"):
			return ".pptx"
		}
	}
	return ""
}

// officeExt 决定复制给 Office 的临时文件用什么扩展名
func officeExt(in string, k appKind) string {
	ext := conv.Ext(in)
	kind := sniff(in)
	switch kind {
	case "zip":
		zk := zipKind(in)
		if zk == "" {
			return ext // ODF（odt ods odp）等
		}
		switch ext {
		case ".doc", ".wps", ".wpt", ".dot", ".xls", ".et", ".ett", ".xlt", ".ppt", ".dps", ".dpt", ".pps", ".pot":
			return zk
		}
	case "ole":
		switch ext {
		case ".wps", ".wpt":
			return ".doc"
		case ".et", ".ett":
			return ".xls"
		case ".dps", ".dpt":
			return ".ppt"
		}
	case "rtf":
		if k == appWord {
			return ".rtf"
		}
	case "html":
		switch ext {
		case ".doc", ".wps", ".xls", ".et":
			return ".htm"
		}
	case "xml":
		switch ext {
		case ".xls", ".et", ".doc", ".wps":
			return ".xml"
		}
	}
	return ext
}
