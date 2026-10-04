package office

import (
	"bytes"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// 文本编码：中文用户的 txt / csv 常见 UTF-8（带或不带 BOM）、UTF-16（记事本的「Unicode」）和 GBK（ANSI）。

type textEncoding int

const (
	encUTF8 textEncoding = iota
	encUTF8BOM
	encUTF16LE
	encUTF16BE
	encGBK
)

func (e textEncoding) String() string {
	return [...]string{"UTF-8", "UTF-8 BOM", "UTF-16LE", "UTF-16BE", "GBK"}[e]
}

// codepage 是 Word 打开文本时用的代码页
func (e textEncoding) codepage() int {
	return [...]int{65001, 65001, 1200, 1201, 936}[e]
}

var bomUTF8 = []byte{0xEF, 0xBB, 0xBF}

// sniffBOM 只看开头的字节序标记
func sniffBOM(b []byte) (textEncoding, bool) {
	switch {
	case bytes.HasPrefix(b, bomUTF8):
		return encUTF8BOM, true
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return encUTF16LE, true
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return encUTF16BE, true
	}
	return 0, false
}

// looksUTF16 判断没有 BOM 的 UTF-16（英文、数字多时每隔一个字节是 0）
func looksUTF16(b []byte) (textEncoding, bool) {
	if len(b) < 8 {
		return 0, false
	}
	n := min(len(b), 4096) &^ 1
	var zeroEven, zeroOdd int
	for i := 0; i < n; i += 2 {
		if b[i] == 0 {
			zeroEven++
		}
		if b[i+1] == 0 {
			zeroOdd++
		}
	}
	half := n / 2
	switch {
	case zeroOdd > half*3/10 && zeroEven < half/20:
		return encUTF16LE, true
	case zeroEven > half*3/10 && zeroOdd < half/20:
		return encUTF16BE, true
	}
	return 0, false
}

// detectEncoding 判断一段完整文本的编码
func detectEncoding(b []byte) textEncoding {
	if e, ok := sniffBOM(b); ok {
		return e
	}
	if e, ok := looksUTF16(b); ok {
		return e
	}
	if utf8.Valid(b) {
		return encUTF8
	}
	return encGBK
}

// detectFileEncoding 判断文件的编码（会把整个文件读一遍，但不全部放进内存）
func detectFileEncoding(path string) (textEncoding, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	head := buf[:n]
	if e, ok := sniffBOM(head); ok {
		return e, nil
	}
	if e, ok := looksUTF16(head); ok {
		return e, nil
	}
	// 逐块检查是不是合法的 UTF-8（块边界上被截断的字符留到下一块）
	var carry []byte
	chunk := head
	for {
		data := append(carry, chunk...)
		cut := incompleteTail(data)
		if !utf8.Valid(data[:len(data)-cut]) {
			return encGBK, nil
		}
		carry = append([]byte(nil), data[len(data)-cut:]...)
		if n < len(buf) {
			break
		}
		n, err = io.ReadFull(f, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return 0, err
		}
		chunk = buf[:n]
		if n == 0 {
			break
		}
	}
	if len(carry) > 0 {
		return encGBK, nil
	}
	return encUTF8, nil
}

// incompleteTail 返回末尾不完整的 UTF-8 字符占了几个字节（0~3）
func incompleteTail(b []byte) int {
	for i := 1; i <= 3 && i <= len(b); i++ {
		c := b[len(b)-i]
		if c < 0x80 {
			return 0
		}
		if c >= 0xC0 { // 一个字符的开头
			need := 2
			if c >= 0xE0 {
				need = 3
			}
			if c >= 0xF0 {
				need = 4
			}
			if need > i {
				return i
			}
			return 0
		}
	}
	return 0
}

// decoder 返回把这种编码转成 UTF-8 的转换器（会去掉 BOM）
func (e textEncoding) decoder() transform.Transformer {
	switch e {
	case encUTF8BOM:
		return unicode.UTF8BOM.NewDecoder()
	case encUTF16LE:
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
	case encUTF16BE:
		return unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder()
	case encGBK:
		return simplifiedchinese.GB18030.NewDecoder()
	}
	return transform.Nop
}

// decodeText 把一段文本转成 Go 字符串
func decodeText(b []byte) (string, textEncoding) {
	e := detectEncoding(b)
	if e == encUTF8 {
		return string(b), e
	}
	s, _, err := transform.Bytes(e.decoder(), b)
	if err != nil {
		return strings.ToValidUTF8(string(b), "�"), e
	}
	return string(s), e
}

// writeUTF8BOM 把文本写成带 BOM 的 UTF-8（Windows 上的 Excel、记事本都能正确识别）
func writeUTF8BOM(path string, s string) error {
	s = strings.TrimPrefix(s, "\uFEFF")
	return os.WriteFile(path, append(append([]byte(nil), bomUTF8...), s...), 0o644)
}

// crlf 统一换行为 \r\n
func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// ensureBOM 把 src（Word / LibreOffice 存的 UTF-8 文本）加上 BOM 写到 dst
func ensureBOM(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	s, _ := decodeText(b)
	if err := writeUTF8BOM(dst, crlf(s)); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}
