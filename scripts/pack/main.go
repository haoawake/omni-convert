// pack 把一个文件夹打成 zip（文件名用 UTF-8 并打上标记，Windows 自带的解压能正确显示中文名）。
//
//	go run ./scripts/pack <文件夹> <输出.zip>
package main

import (
	"archive/zip"
	"compress/flate"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "用法：pack <文件夹> <输出.zip>")
		os.Exit(2)
	}
	src, dst := os.Args[1], os.Args[2]
	out, err := os.Create(dst)
	if err != nil {
		panic(err)
	}
	zw := zip.NewWriter(out)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	base := filepath.Dir(filepath.Clean(src))
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, p)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = rel
		if d.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = out.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
