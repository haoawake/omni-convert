package office

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
)

// 直接修改 OOXML（docx、pptx 都是 zip 包）里的个别部件。只改临时文件夹里的副本，不碰原文件。

// rewriteZip 用 fix 处理 zip 里的每个文件，有改动时重新打包
func rewriteZip(path string, fix func(name string, data []byte) []byte) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	type entry struct {
		hdr  zip.FileHeader
		data []byte
	}
	var entries []entry
	changed := false
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			zr.Close()
			return err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			zr.Close()
			return err
		}
		if nb := fix(f.Name, b); !bytes.Equal(nb, b) {
			b, changed = nb, true
		}
		entries = append(entries, entry{f.FileHeader, b})
	}
	zr.Close()
	if !changed {
		return nil
	}
	tmp := path + ".fix"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	for _, e := range entries {
		h := e.hdr
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(&h)
		if err == nil {
			_, err = w.Write(e.data)
		}
		if err != nil {
			zw.Close()
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Word 打开网页时图片是「链接」到原文件的。断开链接后再缩放图片，Word 又会把链接写回 docx
// （图片既嵌在文档里、又指向临时文件夹里的原图）。这里只保留嵌入的图片，去掉外部链接。

var (
	reBlipLink  = regexp.MustCompile(`(<a:blip\b[^>]*\br:embed="[^"]+"[^>]*?)\s+r:link="[^"]*"`)
	reBlipLink2 = regexp.MustCompile(`(<a:blip\b[^>]*?)\s+r:link="[^"]*"([^>]*\br:embed="[^"]+")`)
	reExtImgRel = regexp.MustCompile(`<Relationship\b[^>]*relationships/image"[^>]*TargetMode="External"[^>]*/>|<Relationship\b[^>]*TargetMode="External"[^>]*relationships/image"[^>]*/>`)
)

// stripLinkedImages 去掉 docx 里图片的外部链接（图片本身已经嵌在文档里）
func stripLinkedImages(path string) error {
	return rewriteZip(path, func(name string, b []byte) []byte {
		switch {
		case strings.HasPrefix(name, "word/_rels/") && strings.HasSuffix(name, ".rels"):
			return reExtImgRel.ReplaceAll(b, nil)
		case strings.HasPrefix(name, "word/") && strings.HasSuffix(name, ".xml"):
			b = reBlipLink.ReplaceAll(b, []byte("$1"))
			return reBlipLink2.ReplaceAll(b, []byte("$1$2"))
		}
		return b
	})
}

// 设置了「修改密码」的 pptx 只能只读打开，而只读打开的演示文稿 PowerPoint 不允许另存（连导出 PDF 都不行）。
// 修改密码只是防止误改的提示，不是加密：在副本里去掉它就能正常转换（打开密码是加密，去不掉，也不会去）。

var reModifyVerifier = regexp.MustCompile(`<p:modifyVerifier\b[^>]*/>`)

// stripModifyPassword 去掉 pptx 副本里的修改密码
func stripModifyPassword(path string) error {
	return rewriteZip(path, func(name string, b []byte) []byte {
		if name == "ppt/presentation.xml" {
			return reModifyVerifier.ReplaceAll(b, nil)
		}
		return b
	})
}
