package catalog

import (
	"strconv"

	"github.com/haoawake/omni-convert/internal/conv"
)

// Check 在开始转换前检查选项有没有填错，返回给用户看的提示；没问题返回空字符串。
// o 是 Prepare 整理过的选项。
func (t *Target) Check(o conv.Options) string {
	if v, ok := o[conv.OptTargetSize]; ok {
		if n, err := strconv.ParseInt(v, 10, 64); err != nil || n <= 0 {
			return "请填上要压缩到多大"
		}
	}
	if o[conv.OptResize] == "box" {
		w, h := o.Int(conv.OptWidth, 0), o.Int(conv.OptHeight, 0)
		if w <= 0 && h <= 0 {
			return "请填上宽和高（像素）"
		}
		if w > 20000 || h > 20000 {
			return "宽和高最大 20000 像素"
		}
		if (w <= 0 || h <= 0) && o.Str(conv.OptFit, "contain") != "contain" {
			return "裁剪填满、留白填充、拉伸需要同时填上宽和高"
		}
	}
	if o[conv.OptResize] == "percent" {
		if p := o.Int(conv.OptPercent, 0); p <= 0 || p > 1000 {
			return "百分比要在 1 到 1000 之间"
		}
	}
	if o[conv.OptResize] == "long" && o.Int(conv.OptLong, 0) <= 0 {
		return "请填上长边的像素数"
	}
	if q, ok := o[conv.OptQuality]; ok && t.Page == conv.Image && q != "" {
		if n, err := strconv.Atoi(q); err != nil || n < 1 || n > 100 {
			return "画质要填 1 到 100 之间的数字（不填就是自动）"
		}
	}
	start, end := o.Seconds(conv.OptStart), o.Seconds(conv.OptEnd)
	if o.Str(conv.OptStart, "") != "" && start < 0 {
		return "截取的开始时间格式不对，可以写成 1:30 或 90"
	}
	if o.Str(conv.OptEnd, "") != "" && end < 0 {
		return "截取的结束时间格式不对，可以写成 1:30 或 90"
	}
	if start >= 0 && end >= 0 && end <= start {
		return "截取的结束时间要比开始时间晚"
	}
	switch t.ID {
	case "pdf:encrypt":
		if o.Str(conv.OptPassword, "") == "" {
			return "请先填上要设置的密码"
		}
	case "pdf:split":
		switch o.Str(conv.OptSplit, "each") {
		case "every":
			if o.Int(conv.OptEvery, 0) <= 0 {
				return "请填上每几页拆成一个文件"
			}
		case "ranges":
			if o.Str(conv.OptRanges, "") == "" {
				return "请填上页码范围，比如 1-3,4-10"
			}
			if _, err := conv.ParseRanges(o[conv.OptRanges], 1<<30); err != nil {
				return err.Error()
			}
		}
	case "doc:mp4":
		if s := o.Int(conv.OptSlideSec, 0); s <= 0 || s > 600 {
			return "每页停留的秒数要在 1 到 600 之间"
		}
	}
	if p := o.Str(conv.OptPages, ""); p != "" {
		if _, err := conv.ParseRanges(p, 1<<30); err != nil {
			return err.Error()
		}
	}
	return ""
}
