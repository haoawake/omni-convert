// Package conv 是所有转换共用的部分：文件分类、选项、任务、输出文件命名。
// 具体怎么转换由 media（图片、视频、音频）、office（Word、Excel、PPT）、pdf 三个包实现，
// catalog 包把它们登记成界面上的一个个「转成 xx」按钮。
package conv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Kind 是文件的大类，也是界面左侧的一页
type Kind int

const (
	Image Kind = iota
	Video
	Audio
	Doc
	PDF
	NumKinds
)

func (k Kind) Name() string {
	return [...]string{"图片", "视频", "音频", "文档", "PDF"}[k]
}

// Family 把文档再细分一下：Word 类、Excel 类、PPT 类……能转成什么取决于它
type Family int

const (
	FamNone     Family = iota
	FamWord            // doc docx rtf odt wps
	FamExcel           // xls xlsx ods et
	FamCSV             // csv（不用 Office 也能处理）
	FamPPT             // ppt pptx odp dps
	FamText            // txt
	FamMarkdown        // md
	FamHTML            // html htm
)

type extInfo struct {
	kind Kind
	fam  Family
}

var exts = map[string]extInfo{}

func reg(k Kind, f Family, list string) {
	for _, e := range strings.Fields(list) {
		exts["."+e] = extInfo{k, f}
	}
}

func init() {
	reg(Image, FamNone, "jpg jpeg jpe jfif png webp bmp dib gif tif tiff heic heif hif avif ico cur tga psd psb svg svgz jxl jp2 j2k dds exr hdr pcx ppm pgm pbm pnm qoi xcf emf wmf "+
		"dng cr2 cr3 crw nef nrw arw srf sr2 orf rw2 raf srw pef x3f 3fr erf kdc mrw mos rwl iiq")
	reg(Video, FamNone, "mp4 m4v mkv mov qt avi webm flv f4v wmv asf ts mts m2ts m2t mpg mpeg mpe vob 3gp 3g2 ogv rm rmvb divx xvid mxf dv y4m")
	reg(Audio, FamNone, "mp3 wav wave flac aac m4a m4b m4r ogg oga opus wma aiff aif aifc ac3 eac3 dts amr awb ape wv mka caf mp2 mpa au snd tta spx gsm ra voc w64")
	reg(Doc, FamWord, "doc docx docm dot dotx dotm rtf odt ott wps wpt")
	reg(Doc, FamExcel, "xls xlsx xlsm xlsb xlt xltx ods ots et ett")
	reg(Doc, FamCSV, "csv tsv")
	reg(Doc, FamPPT, "ppt pptx pptm pps ppsx pot potx odp otp dps dpt")
	reg(Doc, FamText, "txt text log")
	reg(Doc, FamMarkdown, "md markdown")
	reg(Doc, FamHTML, "html htm xhtml mht mhtml")
	reg(PDF, FamNone, "pdf")
}

// Ext 返回小写的扩展名（带点）
func Ext(path string) string { return strings.ToLower(filepath.Ext(path)) }

// KindOf 按扩展名判断文件属于哪一类
func KindOf(path string) (Kind, bool) {
	i, ok := exts[Ext(path)]
	return i.kind, ok
}

// FamilyOf 判断文档的细分类型，不是文档返回 FamNone
func FamilyOf(path string) Family { return exts[Ext(path)].fam }

func IsImage(path string) bool { k, ok := KindOf(path); return ok && k == Image }
func IsVideo(path string) bool { k, ok := KindOf(path); return ok && k == Video }
func IsAudio(path string) bool { k, ok := KindOf(path); return ok && k == Audio }
func IsPDF(path string) bool   { return Ext(path) == ".pdf" }

// IsAnimated 粗略判断：这种格式通常是动图（gif 可能是静态的，转换器自己再细看）
func IsAnimated(path string) bool {
	switch Ext(path) {
	case ".gif", ".apng":
		return true
	}
	return false
}

// ExtsOf 列出某一类的所有扩展名（不带点），给「添加文件」对话框做筛选用
func ExtsOf(k Kind) []string {
	var out []string
	for e, i := range exts {
		if i.kind == k {
			out = append(out, e[1:])
		}
	}
	return out
}

// ---------------------------------------------------------------- 任务

// RunFunc 执行一个任务。返回的错误信息会直接显示给用户，要写成看得懂的中文。
type RunFunc func(ctx context.Context, j *Job) error

// Progress 是任务的进度
type Progress struct {
	Frac float64 // 0~1，小于 0 表示说不准（界面显示成转圈）
	Note string  // 补充说明，比如「第 3/12 页」「正在启动 Word」
}

// Job 是一个转换任务：通常一个输入文件对应一个任务；合并类的任务一次处理多个文件。
type Job struct {
	Inputs []string // 输入文件的完整路径
	Target string   // 目标 ID，比如 "img:jpg"
	Opt    Options
	OutDir string // 输出文件夹，已经存在

	mu       sync.Mutex
	outputs  []string // 已经占用的输出文件或文件夹（失败时由执行者删掉）
	temps    []string
	progress func(Progress)
}

// NewJob 创建任务。onProgress 可以为 nil。
func NewJob(inputs []string, target string, opt Options, outDir string, onProgress func(Progress)) *Job {
	if opt == nil {
		opt = Options{}
	}
	return &Job{Inputs: inputs, Target: target, Opt: opt, OutDir: outDir, progress: onProgress}
}

// Input 是第一个（通常也是唯一一个）输入文件
func (j *Job) Input() string { return j.Inputs[0] }

// Base 是第一个输入文件去掉扩展名的文件名
func (j *Job) Base() string {
	b := filepath.Base(j.Input())
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// Report 汇报进度，可以从任意 goroutine 调用
func (j *Job) Report(frac float64, note string) {
	if j.progress != nil {
		j.progress(Progress{Frac: frac, Note: note})
	}
}

// Outputs 是这个任务写出的文件和文件夹
func (j *Job) Outputs() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.outputs...)
}

// OutFile 在输出文件夹里占一个不重名的文件名 Base()+ext（ext 带点，比如 ".jpg"）。
// 重名时依次试「名字 (1).jpg」「名字 (2).jpg」……永远不会覆盖已有文件（包括输入文件本身）。
// 占到的文件名记在任务的输出里：任务失败或取消时会被删掉。
func (j *Job) OutFile(ext string) string { return j.OutFileNamed(j.Base(), ext) }

// OutFileNamed 同 OutFile，但自己指定文件名（不含扩展名）
func (j *Job) OutFileNamed(base, ext string) string {
	p := reserve(j.OutDir, base, ext, false)
	j.mu.Lock()
	j.outputs = append(j.outputs, p)
	j.mu.Unlock()
	return p
}

// OutFolder 在输出文件夹里新建一个不重名的文件夹（多页 PDF 转图片、拆分 PDF 这类会产生很多文件的任务用）。
func (j *Job) OutFolder(name string) (string, error) {
	p := reserve(j.OutDir, name, "", true)
	if err := os.MkdirAll(p, 0o755); err != nil {
		release(p)
		return "", err
	}
	j.mu.Lock()
	j.outputs = append(j.outputs, p)
	j.mu.Unlock()
	return p, nil
}

// AddOutput 记下一个由转换器自己决定路径的输出（比如写在 OutFolder 里的文件不需要再记）
func (j *Job) AddOutput(path string) {
	j.mu.Lock()
	j.outputs = append(j.outputs, path)
	j.mu.Unlock()
}

// TempDir 给这个任务建一个临时文件夹，任务结束后自动删除
func (j *Job) TempDir() (string, error) {
	d, err := os.MkdirTemp("", "omni-convert-*")
	if err != nil {
		return "", err
	}
	j.mu.Lock()
	j.temps = append(j.temps, d)
	j.mu.Unlock()
	return d, nil
}

// Finish 收尾：删临时文件夹、释放占用的文件名。failed 为 true 时把已经写出的结果也删掉，
// 免得留下转了一半的坏文件。
func (j *Job) Finish(failed bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, d := range j.temps {
		os.RemoveAll(d)
	}
	j.temps = nil
	for _, p := range j.outputs {
		if failed {
			os.RemoveAll(p)
		}
		release(p)
	}
	if failed {
		j.outputs = nil
	}
}

// 正在使用（还没写完）的输出文件名，防止两个同时进行的任务挑中同一个名字
var (
	reservedMu sync.Mutex
	reserved   = map[string]bool{}
)

// maxBase 是文件名（不含扩展名和「 (1)」）最多保留的字符数。Windows 一段路径最长 255 个字符，
// 留出加后缀、编号的余量；浏览器下载的超长文件名会被截短。
const maxBase = 200

func reserve(dir, base, ext string, folder bool) string {
	reservedMu.Lock()
	defer reservedMu.Unlock()
	base = SafeName(base)
	if r := []rune(base); len(r) > maxBase {
		base = strings.TrimRight(string(r[:maxBase]), " .")
	}
	for i := 0; ; i++ {
		name := base + ext
		if i > 0 {
			name = base + " (" + itoa(i) + ")" + ext
		}
		p := filepath.Join(dir, name)
		key := strings.ToLower(p)
		if reserved[key] {
			continue
		}
		_, err := os.Lstat(p)
		// 查不了（比如没有权限）的名字也跳过，但别无限地试下去：实在不行就交给写文件时报错
		if (err == nil || !errors.Is(err, os.ErrNotExist)) && i < 10000 {
			continue
		}
		reserved[key] = true
		return p
	}
}

func release(p string) {
	reservedMu.Lock()
	delete(reserved, strings.ToLower(p))
	reservedMu.Unlock()
}

// SafeName 去掉文件名里 Windows 不允许的字符
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(s, " .")
	if s == "" {
		s = "未命名"
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// ---------------------------------------------------------------- 错误

// ErrCancelled 表示用户点了停止
var ErrCancelled = errors.New("已取消")

// Fail 生成一个给用户看的错误：msg 是一句话说明，detail 是附加的技术细节（可以为空）
func Fail(msg, detail string) error { return &UserError{Msg: msg, Detail: detail} }

// UserError 是给用户看的错误，Detail 放进「详细信息」里
type UserError struct {
	Msg    string
	Detail string
}

func (e *UserError) Error() string {
	if e.Detail == "" {
		return e.Msg
	}
	return e.Msg + "（" + e.Detail + "）"
}
