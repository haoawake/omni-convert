package conv

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		`C:\a\照片.HEIC`: Image, "b.jpg": Image, "c.NEF": Image,
		"d.mkv": Video, "e.MP4": Video,
		"f.flac": Audio, "g.m4a": Audio,
		"h.docx": Doc, "i.xlsx": Doc, "j.pptx": Doc, "k.md": Doc, "l.csv": Doc,
		"m.PDF": PDF,
	}
	for p, want := range cases {
		got, ok := KindOf(p)
		if !ok || got != want {
			t.Errorf("KindOf(%q) = %v,%v，应该是 %v", p, got, ok, want)
		}
	}
	if _, ok := KindOf("x.exe"); ok {
		t.Error("exe 不该被认出来")
	}
	if FamilyOf("a.xlsx") != FamExcel || FamilyOf("a.ppt") != FamPPT || FamilyOf("a.rtf") != FamWord {
		t.Error("文档细分类型不对")
	}
}

func TestParseRanges(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []int
	}{
		{"", 3, []int{1, 2, 3}},
		{"1-3,5", 10, []int{1, 2, 3, 5}},
		{"8-", 10, []int{8, 9, 10}},
		{"3-1", 10, []int{1, 2, 3}},
		{"1，3、5", 10, []int{1, 3, 5}},
		{"2~4", 10, []int{2, 3, 4}},
		{"5-20", 7, []int{5, 6, 7}},
		{"1-2,2-3", 5, []int{1, 2, 3}},
		{"-2", 5, []int{1, 2}},
	}
	for _, c := range cases {
		r, err := ParseRanges(c.in, c.n)
		if err != nil {
			t.Errorf("ParseRanges(%q): %v", c.in, err)
			continue
		}
		if got := Pages(r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseRanges(%q, %d) = %v，应该是 %v", c.in, c.n, got, c.want)
		}
	}
	for _, bad := range []string{"abc", "20-30", "1-x"} {
		if _, err := ParseRanges(bad, 10); err == nil {
			t.Errorf("ParseRanges(%q) 应该报错", bad)
		}
	}
}

func TestParseSizeClock(t *testing.T) {
	sizes := map[string]int64{"500KB": 500 * 1024, "1.5 MB": 1536 * 1024, "800k": 800 * 1024, "2m": 2 << 20, "1234": 1234}
	for s, want := range sizes {
		if got, ok := ParseSize(s); !ok || got != want {
			t.Errorf("ParseSize(%q) = %d，应该是 %d", s, got, want)
		}
	}
	clocks := map[string]float64{"83.5": 83.5, "1:23.5": 83.5, "0:01:23": 83, "1：00": 60}
	for s, want := range clocks {
		if got, ok := ParseClock(s); !ok || got != want {
			t.Errorf("ParseClock(%q) = %v，应该是 %v", s, got, want)
		}
	}
	if _, ok := ParseClock("abc"); ok {
		t.Error("abc 不是时间")
	}
}

func TestOutFileNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "照片.jpg")
	os.WriteFile(in, []byte("x"), 0o644)
	j := NewJob([]string{in}, "img:jpg", nil, dir, nil)
	a := j.OutFile(".jpg")
	b := j.OutFile(".jpg") // 第一个还没写出来，也不能重名
	if filepath.Base(a) != "照片 (1).jpg" || filepath.Base(b) != "照片 (2).jpg" {
		t.Fatalf("得到 %s 和 %s", a, b)
	}
	if p := j.OutFile(".png"); filepath.Base(p) != "照片.png" {
		t.Fatalf("得到 %s", p)
	}
	os.WriteFile(a, []byte("half"), 0o644)
	j.Finish(true)
	if _, err := os.Stat(a); err == nil {
		t.Fatal("失败的任务应该删掉写了一半的文件")
	}
	if _, err := os.Stat(in); err != nil {
		t.Fatal("输入文件不能动")
	}
	// 释放后名字可以再用
	j2 := NewJob([]string{in}, "img:jpg", nil, dir, nil)
	if p := j2.OutFile(".jpg"); filepath.Base(p) != "照片 (1).jpg" {
		t.Fatalf("得到 %s", p)
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName(`a<b>:c?. `); got != "a_b__c_" {
		t.Errorf("得到 %q", got)
	}
	if SafeName("...") != "未命名" {
		t.Error("全是点的名字要换掉")
	}
}

func TestOutFileLongName(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("很长的名字", 60) // 300 个字符，超过 Windows 一段路径的上限
	j := NewJob([]string{filepath.Join(dir, long+".pdf")}, "pdf:compress", nil, dir, nil)
	done := make(chan string, 1)
	go func() { done <- j.OutFileNamed(j.Base()+"_压缩", ".pdf") }()
	select {
	case p := <-done:
		if n := len([]rune(filepath.Base(p))); n > 255 {
			t.Fatalf("文件名太长（%d 个字符）", n)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("截短后的名字应该能写：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("挑文件名卡住了")
	}
}
