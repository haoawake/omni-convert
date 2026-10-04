// Package catalog 登记界面上的每一种转换：左侧每一页（图片、视频、音频、文档、PDF）有哪些「转成 xx」按钮，
// 每个按钮接受哪些文件、需要哪些选项，以及最终交给哪个转换器去做（见 bind.go）。
package catalog

import (
	"strconv"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
)

// FieldType 是选项控件的种类
type FieldType int

const (
	Select   FieldType = iota // 下拉选择
	Seg                       // 并排的几个按钮，选一个
	Number                    // 数字输入框
	Text                      // 文字输入框
	Password                  // 密码输入框
	Check                     // 勾选框
	Static                    // 一小段文字（比如宽高中间的「×」）
)

type Choice struct {
	Value string
	Label string
}

// Field 是一个选项控件
type Field struct {
	Key         string
	Type        FieldType
	Label       string // 勾选框的文字、Static 的文字
	Choices     []Choice
	Default     string
	Unit        string // 输入框后面的单位
	Width       int    // 输入框、下拉框的宽度（96 DPI 下的像素），0 = 自动
	Placeholder string
	ShowIf      func(o conv.Options) bool
	// Apply 用于「常用尺寸」这种预设：选中后改写其他选项，自己不保存
	Apply func(o conv.Options, value string)
}

// Row 是选项区的一行
type Row struct {
	Label  string
	Fields []Field
	Note   string // 灰色的补充说明
	ShowIf func(o conv.Options) bool
}

// Target 是一个转换目标（界面上的一个按钮）
type Target struct {
	ID     string
	Page   conv.Kind
	Label  string
	Hint   string // 选中后显示在按钮下面的一句话说明
	Tool   bool   // 是「工具」（合并、拆分……）而不是「转成某种格式」
	Batch  bool   // 一次处理整个列表，只产生一个结果
	Lane   string
	Rows   []Row
	Accept func(path string) bool
	Run    conv.RunFunc
}

// Page 是左侧的一页
type Page struct {
	Kind    conv.Kind
	Title   string
	Sub     string // 页面标题下面的说明
	Targets []*Target
}

var (
	Pages   []*Page
	targets = map[string]*Target{}
)

// ByID 找到某个转换目标
func ByID(id string) *Target { return targets[id] }

// PageOf 找到某一类文件对应的页
func PageOf(k conv.Kind) *Page {
	for _, p := range Pages {
		if p.Kind == k {
			return p
		}
	}
	return nil
}

func add(p *Page, t *Target) {
	t.Page = p.Kind
	p.Targets = append(p.Targets, t)
	targets[t.ID] = t
}

// Defaults 在 o 里补上这个目标所有选项的默认值（已经有值的不动），
// 并把不在可选范围里的值（换了目标、或者旧版本存下的设置）改回默认值。
func (t *Target) Defaults(o conv.Options) {
	for _, r := range t.Rows {
		for _, f := range r.Fields {
			if f.Key == "" || f.Apply != nil || f.Type == Static {
				continue
			}
			v, ok := o[f.Key]
			if ok && len(f.Choices) > 0 && !hasChoice(f.Choices, v) {
				ok = false
			}
			if !ok {
				if f.Default != "" {
					o[f.Key] = f.Default
				} else {
					delete(o, f.Key)
				}
			}
		}
	}
}

func hasChoice(cs []Choice, v string) bool {
	for _, c := range cs {
		if c.Value == v {
			return true
		}
	}
	return false
}

// Visible 列出当前选项下要显示的行和控件
func (t *Target) Visible(o conv.Options) []Row {
	var out []Row
	for _, r := range t.Rows {
		if r.ShowIf != nil && !r.ShowIf(o) {
			continue
		}
		rr := r
		rr.Fields = nil
		for _, f := range r.Fields {
			if f.ShowIf == nil || f.ShowIf(o) {
				rr.Fields = append(rr.Fields, f)
			}
		}
		out = append(out, rr)
	}
	return out
}

// Prepare 把界面上的选项整理成转换器要的样子：补默认值、只保留看得见的选项、
// 把「文件大小」「分辨率」这类预设换算成具体的数值。
func (t *Target) Prepare(ui conv.Options) conv.Options {
	src := ui.Clone()
	t.Defaults(src)
	o := conv.Options{}
	for _, r := range t.Visible(src) {
		for _, f := range r.Fields {
			if f.Key != "" && f.Apply == nil && f.Type != Static {
				o[f.Key] = src[f.Key]
			}
		}
	}
	// 文件大小
	if v, ok := o[keySizePreset]; ok {
		delete(o, keySizePreset)
		num, unit := o[keySizeNum], o[keySizeUnit]
		delete(o, keySizeNum)
		delete(o, keySizeUnit)
		switch v {
		case "", "0":
		case "custom":
			if n, err := strconv.ParseFloat(strings.TrimSpace(num), 64); err == nil && n > 0 {
				mul := 1024.0
				if unit == "MB" {
					mul = 1024 * 1024
				}
				o[conv.OptTargetSize] = strconv.FormatInt(int64(n*mul), 10)
			} else {
				o[conv.OptTargetSize] = "?" // 选了自定义但没填，Check 会提示
			}
		default:
			o[conv.OptTargetSize] = v
		}
	}
	// 动图的宽度和帧率
	if v, ok := o[keyAnimW]; ok {
		delete(o, keyAnimW)
		if v == "keep" || v == "" {
			o[conv.OptResize] = "none"
		} else {
			o[conv.OptResize], o[conv.OptWidth], o[conv.OptHeight] = "box", v, "0"
		}
	}
	if v, ok := o[keyAnimFPS]; ok {
		delete(o, keyAnimFPS)
		o[conv.OptFPS] = v
	}
	// 加密时设置的新密码
	if v, ok := o[keyNewPassword]; ok {
		delete(o, keyNewPassword)
		if v != "" {
			o[conv.OptPassword] = v
		}
	}
	// 视频分辨率预设
	if v, ok := o[keyRes]; ok {
		delete(o, keyRes)
		switch {
		case v == "" || v == "keep":
			o[conv.OptResize] = "none"
			delete(o, conv.OptFit)
		case v == "custom":
			o[conv.OptResize] = "box"
		default:
			w, h, _ := strings.Cut(v, "x")
			o[conv.OptResize] = "box"
			o[conv.OptWidth], o[conv.OptHeight] = w, h
		}
	}
	return o
}

// 只在界面上用、交给转换器前会被换算掉的键
const (
	keySizePreset  = "sizepreset"
	keySizeNum     = "sizenum"
	keySizeUnit    = "sizeunit"
	keyRes         = "res"
	keyPreset      = "preset"
	keyAnimW       = "animw"
	keyAnimFPS     = "animfps"
	keyNewPassword = "newpw"
)

// Accepts 判断某个文件能不能放进这一页的列表（这一页至少有一个目标能处理它）
func (p *Page) Accepts(path string) bool {
	for _, t := range p.Targets {
		if t.Accept != nil && t.Accept(path) {
			return true
		}
	}
	return false
}

// Exts 是「添加文件」对话框里这一页能选的文件类型
func (p *Page) Exts() []string {
	seen := map[string]bool{}
	var out []string
	addKind := func(k conv.Kind) {
		for _, e := range conv.ExtsOf(k) {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	addKind(p.Kind)
	switch p.Kind {
	case conv.Audio:
		addKind(conv.Video) // 从视频里提取声音
	case conv.Video:
		if !seen["gif"] {
			out = append(out, "gif")
		}
	}
	return out
}
