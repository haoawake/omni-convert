package office

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 「用哪个程序转换」的抽象：Microsoft Office / WPS（COM 自动化）或者 LibreOffice（命令行）。

type appKind int

const (
	appWord appKind = iota
	appExcel
	appPPT
	numApps
)

const (
	vendorMS  = "Microsoft Office"
	vendorWPS = "WPS"
	vendorLO  = "LibreOffice"
)

// progInfo 是一个可以用 COM 自动化的程序
type progInfo struct {
	vendor string // vendorMS 或 vendorWPS
	progID string // 比如 Word.Application
	exe    string // 进程名，比如 WINWORD.EXE
	path   string // 程序的完整路径
}

func (p progInfo) appName(k appKind) string {
	if p.vendor == vendorWPS {
		return [...]string{"WPS 文字", "WPS 表格", "WPS 演示"}[k]
	}
	return [...]string{"Word", "Excel", "PowerPoint"}[k]
}

// engine 用某个程序打开文件、另存为别的格式
type engine interface {
	name() string
	// save 打开 in，另存为 out（format 是目标扩展名，不带点：pdf docx xlsx…）
	save(t *task, in, out, format string, o openOpts) error
}

// videoEngine 能把演示文稿导出成视频（只有 PowerPoint 可以）
type videoEngine interface {
	video(t *task, in, out string, slideSec int) error
}

// openOpts 是打开文件时的特殊要求
type openOpts struct {
	codepage  int  // 纯文本的编码（65001 = UTF-8）
	text      bool // 纯文本：换成合适的中文字体、紧凑行距
	web       bool // 网页：把链接的图片存进文档，切换到页面视图
	pdf       bool // 输入是 PDF（Word 会重排成可编辑文档，比较慢）
	gridlines bool // 表格转 PDF 时打印网格线（CSV 转来的表格）
}

// unavailableError 表示程序没法启动（没装好、没激活、被删了），可以换下一个程序试试
type unavailableError struct {
	app string
	err error
}

func (e *unavailableError) Error() string {
	return "没法启动 " + e.app + "：" + e.err.Error()
}

var errUnavailable = &unavailableError{app: "Office", err: errors.New("没有安装")}

// forceEngine 让测试指定只用哪种程序："" 自动，"none" 什么都不用，"soffice" 只用 LibreOffice，"com" 只用 Office/WPS
var forceEngine = ""

// forceNoEdge 让测试假装没有 Edge
var forceNoEdge = false

func enginesFor(k appKind) []engine {
	var es []engine
	if forceEngine == "none" {
		return nil
	}
	if forceEngine != "soffice" {
		if p := comProg(k); p.progID != "" {
			if e := newCOMEngine(k); e != nil {
				es = append(es, e)
			}
		}
	}
	if forceEngine != "com" {
		if lo := Detect().LibreOffice; lo != "" {
			es = append(es, sofficeEngine{exe: lo})
		}
	}
	return es
}

// hasCOM 判断某个程序能不能用 COM 自动化（Microsoft Office 或 WPS）
func hasCOM(k appKind) bool {
	return forceEngine != "none" && forceEngine != "soffice" && comProg(k).progID != ""
}

func edgePath() string {
	if forceNoEdge {
		return ""
	}
	return Detect().Edge
}

func errNoOffice() error {
	if runtime.GOOS != "windows" {
		return conv.Fail("这个功能只支持 Windows", "")
	}
	return conv.Fail("需要安装 Microsoft Office、WPS 或 LibreOffice 才能转换这类文件", "LibreOffice 是免费的：https://zh-cn.libreoffice.org")
}

// withEngine 依次用能用的程序试，程序启动不了就换下一个；文件本身的问题（有密码、损坏）直接返回
func withEngine(k appKind, fn func(e engine) error) error {
	es := enginesFor(k)
	if len(es) == 0 {
		return errNoOffice()
	}
	var last error
	for _, e := range es {
		err := fn(e)
		var ue *unavailableError
		if errors.As(err, &ue) {
			last = err
			continue
		}
		return err
	}
	return conv.Fail("Office 软件没法启动", last.Error())
}

// ---------------------------------------------------------------- 任务的上下文

const tempPrefix = "omni-convert-"

// tempMarker 出现在我们所有临时文件的路径里（清理 Office 崩溃记录时用来认出自己的文件）
const tempMarker = "omni-convert-"

type task struct {
	ctx    context.Context
	opt    conv.Options
	report func(frac float64, note string)
	mkTemp func() (string, error)
	tmp    string
	n      int
	own    []string // 自己建的临时文件夹（没有 Job 时）
}

func newTask(ctx context.Context, j *conv.Job) *task {
	if ctx == nil {
		ctx = context.Background()
	}
	t := &task{ctx: ctx, opt: conv.Options{}, report: func(float64, string) {}}
	if j != nil {
		t.opt = j.Opt
		t.report = j.Report
		t.mkTemp = j.TempDir
	} else {
		t.mkTemp = func() (string, error) {
			d, err := os.MkdirTemp("", tempPrefix+"*")
			if err == nil {
				t.own = append(t.own, d)
			}
			return d, err
		}
	}
	return t
}

// close 删掉自己建的临时文件夹
func (t *task) close() {
	for _, d := range t.own {
		os.RemoveAll(d)
	}
	t.own = nil
}

// tempPath 在任务的临时文件夹里取一个不重复的名字（只用英文和数字，避免 Office 对路径挑剔）
func (t *task) tempPath(stem, ext string) (string, error) {
	if t.tmp == "" {
		d, err := t.mkTemp()
		if err != nil {
			return "", fmt.Errorf("没法创建临时文件夹：%w", err)
		}
		t.tmp = d
	}
	t.n++
	return filepath.Join(t.tmp, fmt.Sprintf("%s%d%s", stem, t.n, ext)), nil
}

// tempDir 在临时文件夹里建一个子文件夹
func (t *task) tempDir(stem string) (string, error) {
	p, err := t.tempPath(stem, "")
	if err != nil {
		return "", err
	}
	return p, os.MkdirAll(p, 0o755)
}

// copyIn 把输入复制到临时文件夹（换成短的英文名、指定的扩展名）。
// 这样 Office 不会碰到超长路径、奇怪字符、被别的程序占用、「来自网络」标记这些问题，也绝不会改动原文件。
func (t *task) copyIn(in, ext string) (string, error) {
	dst, err := t.tempPath("src", ext)
	if err != nil {
		return "", err
	}
	if err := copyFile(in, dst); err != nil {
		return "", conv.Fail("没法读取文件", err.Error())
	}
	return dst, nil
}

func (t *task) cancelled() bool { return t.ctx.Err() != nil }

func copyFile(src, dst string) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		os.Remove(dst)
		return err
	}
	return w.Close()
}

// moveFile 把临时结果挪到最终位置（不在同一个盘时复制）
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return conv.Fail("没法写入输出文件", err.Error())
	}
	os.Remove(src)
	return nil
}

// checkOutput 确认程序真的写出了文件
func checkOutput(p, app string) error {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() == 0 {
		return conv.Fail(app+" 没有生成文件", "")
	}
	return nil
}

func upper(s string) string { return strings.ToUpper(strings.TrimPrefix(s, ".")) }
