package pdf

// 合并、拆分、旋转、加密、解密：主要靠 pdfcpu。
// pdfcpu 对文件比较挑剔，读不了的时候先用 pdfium 另存一份（顺便去掉加密），再交给 pdfcpu。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/haoawake/omni-convert/internal/conv"
)

// newConf 生成 pdfcpu 的配置：不读写配置文件夹、宽松校验、不联网
func newConf(password string, cmd model.CommandMode) *model.Configuration {
	c := model.NewStatelessConfiguration()
	c.ValidationMode = model.ValidationRelaxed
	c.Offline = true
	c.UserPW = password
	c.OwnerPW = password
	c.Cmd = cmd
	return c
}

// readCtx 用 pdfcpu 读入并校验一个 PDF；conf.Optimize 为 true 时顺便优化（去掉重复的字体、图片等）
func readCtx(c context.Context, path string, conf *model.Configuration) (*model.Context, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, conv.Fail("读不了这个文件", err.Error())
	}
	defer f.Close()
	if !conf.Optimize {
		return api.ReadAndValidate(c, f, conf)
	}
	return api.ReadValidateAndOptimize(c, f, conf, nil)
}

// pdfcpuErr 把 pdfcpu 的错误翻译成中文
func pdfcpuErr(err error, password string) error {
	switch {
	case err == nil:
		return nil
	case isUserErr(err):
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return conv.ErrCancelled
	case errors.Is(err, pdfcpu.ErrWrongPassword):
		if password == "" {
			return errNeedPassword
		}
		return errWrongPassword
	case errors.Is(err, pdfcpu.ErrNotEncrypted):
		return conv.Fail("这个 PDF 没有设置密码，不需要解除", "")
	}
	return conv.Fail("PDF 文件有问题，处理不了", err.Error())
}

// withRepair 先直接用 pdfcpu 处理 in；失败了（文件不规范、有权限限制、密码编码不同……）
// 就用 pdfium 打开、另存一份去掉加密的副本，再处理一次。op 收到的 password 是打开 in 要用的密码。
func withRepair(c context.Context, j *conv.Job, in, password string, op func(in, password string) error) error {
	err := safe(func() error { return op(in, password) })
	if err == nil {
		return nil
	}
	if c.Err() != nil {
		return conv.ErrCancelled
	}
	if isUserErr(err) || pdfiumAvailable() != nil {
		return pdfcpuErr(err, password)
	}
	fixed, ferr := repairedCopy(j, in, password)
	if ferr != nil {
		// pdfium 也打不开：它给的原因（要密码、密码不对、文件坏了）更准确
		return ferr
	}
	if err2 := safe(func() error { return op(fixed, "") }); err2 != nil {
		if c.Err() != nil {
			return conv.ErrCancelled
		}
		if isUserErr(err2) {
			return err2
		}
		return pdfcpuErr(err, password)
	}
	return nil
}

// repairedCopy 用 pdfium 另存一份去掉加密的副本，放在任务的临时文件夹里
func repairedCopy(j *conv.Job, in, password string) (string, error) {
	d, err := Open(in, password)
	if err != nil {
		return "", err
	}
	defer d.Close()
	tmp, err := j.TempDir()
	if err != nil {
		return "", conv.Fail("没法创建临时文件夹", err.Error())
	}
	fixed := filepath.Join(tmp, "repaired.pdf")
	if err := d.SaveCopy(fixed); err != nil {
		return "", err
	}
	return fixed, nil
}

// moveFile 把 src 挪到 dst（不在同一个盘时改成复制）
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

func pdfcpuPageCount(path, password string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, conv.Fail("读不了这个文件", err.Error())
	}
	defer f.Close()
	n, err := api.PageCount(context.Background(), f, newConf(password, model.LISTINFO))
	if err != nil {
		return 0, pdfcpuErr(err, password)
	}
	return n, nil
}

// baseName 是去掉扩展名的文件名
func baseName(path string) string {
	b := filepath.Base(path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// fileErr 在错误前面加上是哪个文件出的问题（合并多个文件时用）
func fileErr(path string, err error) error {
	var ue *conv.UserError
	if errors.As(err, &ue) {
		return conv.Fail("「"+filepath.Base(path)+"」"+ue.Msg, ue.Detail)
	}
	return err
}

// ---------------------------------------------------------------- 合并

// Merge 按顺序把所有输入的 PDF 合并成一个，文件名是「第一个文件名等N个文件_合并.pdf」。
// 合并后的书签里每个原文件占一项。
func Merge() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		n := len(j.Inputs)
		if n < 2 {
			return conv.Fail("至少要选两个 PDF 才能合并", "")
		}
		for _, in := range j.Inputs {
			if !conv.IsPDF(in) {
				return conv.Fail("只能合并 PDF 文件："+filepath.Base(in), "图片请先用「图片转 PDF」")
			}
		}
		pw := j.Opt.Str(conv.OptPassword, "")
		out := j.OutFileNamed(fmt.Sprintf("%s等%d个文件_合并", j.Base(), n), ".pdf")

		err := safe(func() error { return mergePdfcpu(ctx, j, pw, out) })
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return conv.ErrCancelled
		case isUserErr(err):
			return err
		case pdfiumAvailable() != nil:
			return pdfcpuErr(err, pw)
		}
		// pdfcpu 合并不了（个别文件结构太怪），改用 pdfium 逐页复制（书签会丢）
		return mergePdfium(ctx, j, pw, out)
	})
}

func mergePdfcpu(c context.Context, j *conv.Job, pw, out string) error {
	n := len(j.Inputs)
	conf := newConf(pw, model.MERGECREATE)
	conf.Optimize = false // 合并完再统一优化
	conf.CreateBookmarks = true
	conf.MergeBookmarkMode = model.MergeBookmarkModeWrap
	var dest *model.Context
	for i, in := range j.Inputs {
		if err := cancelled(c); err != nil {
			return err
		}
		j.Report(float64(i)/float64(n+1), fmt.Sprintf("正在读第 %d/%d 个文件", i+1, n))
		src, err := readForMerge(c, j, in, pw, conf)
		if err != nil {
			return err
		}
		title := baseName(in)
		if dest == nil {
			if err := pdfcpu.EnsureOutlines(c, src, title, false); err != nil {
				return err
			}
			src.EnsureVersionForWriting()
			dest = src
			continue
		}
		if err := pdfcpu.MergeXRefTables(c, title, src, dest, false, false); err != nil {
			return err
		}
	}
	j.Report(float64(n)/float64(n+1), "正在写入合并后的文件")
	if err := api.OptimizeContext(c, dest); err != nil {
		return err
	}
	return writeFile(out, func(w io.Writer) error { return api.WriteContext(c, dest, w) })
}

// readForMerge 读入一个要合并的文件；加密了或者 pdfcpu 读不了的，先用 pdfium 另存一份
func readForMerge(c context.Context, j *conv.Job, in, pw string, conf *model.Configuration) (*model.Context, error) {
	ctx, err := readCtx(c, in, conf)
	if err == nil {
		return ctx, nil
	}
	if c.Err() != nil {
		return nil, conv.ErrCancelled
	}
	if pdfiumAvailable() != nil {
		return nil, fileErr(in, pdfcpuErr(err, pw))
	}
	fixed, ferr := repairedCopy(j, in, pw)
	if ferr != nil {
		return nil, fileErr(in, ferr)
	}
	c2 := newConf("", model.MERGECREATE)
	c2.Optimize = false
	ctx, err = readCtx(c, fixed, c2)
	if err != nil {
		return nil, fileErr(in, pdfcpuErr(err, ""))
	}
	ctx.Configuration = conf
	return ctx, nil
}

// mergePdfium 用 pdfium 合并：把每个文件的所有页复制进一个新文档
func mergePdfium(c context.Context, j *conv.Job, pw, out string) error {
	n := len(j.Inputs)
	docs := make([]*Doc, 0, n)
	defer func() {
		for _, d := range docs {
			d.Close()
		}
	}()
	for i, in := range j.Inputs {
		if err := cancelled(c); err != nil {
			return err
		}
		j.Report(float64(i)/float64(n+1), fmt.Sprintf("正在读第 %d/%d 个文件", i+1, n))
		d, err := Open(in, pw)
		if err != nil {
			return fileErr(in, err)
		}
		docs = append(docs, d)
	}
	j.Report(float64(n)/float64(n+1), "正在写入合并后的文件")
	return importPages(out, docs, make([][]int, n))
}

// ---------------------------------------------------------------- 拆分

// span 是拆分出来的一段页（从 1 开始，含两端）
type span struct{ from, to int }

func (s span) name(base string) string {
	if s.from == s.to {
		return fmt.Sprintf("%s_第%d页", base, s.from)
	}
	return fmt.Sprintf("%s_%d-%d页", base, s.from, s.to)
}

// Split 拆分 PDF。split 选项：each 每页一个文件（默认）| every 每 N 页一个（every 选项）|
// ranges 按 ranges 选项里写的范围，每段一个文件（只写了一段时直接输出一个文件，相当于「提取页面」）。
func Split() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		in := j.Input()
		pw := j.Opt.Str(conv.OptPassword, "")
		n, err := PageCount(in, pw)
		if err != nil {
			return err
		}
		var spans []span
		switch strings.ToLower(j.Opt.Str(conv.OptSplit, "each")) {
		case "every":
			every := j.Opt.Int(conv.OptEvery, 0)
			if every < 1 {
				return conv.Fail("请填上每几页拆成一个文件", "")
			}
			for p := 1; p <= n; p += every {
				spans = append(spans, span{p, min(p+every-1, n)})
			}
		case "ranges", "range":
			s := j.Opt.Str(conv.OptRanges, "")
			if s == "" {
				s = j.Opt.Str(conv.OptPages, "")
			}
			if s == "" {
				return conv.Fail("请填上要拆出来的页码范围，比如 1-3,4-10", "")
			}
			rs, err := conv.ParseRanges(s, n)
			if err != nil {
				return err
			}
			for _, r := range rs {
				spans = append(spans, span{r.From, r.To})
			}
		default:
			for p := 1; p <= n; p++ {
				spans = append(spans, span{p, p})
			}
		}

		base := j.Base()
		var paths []string
		if len(spans) == 1 {
			paths = []string{j.OutFileNamed(spans[0].name(base), ".pdf")}
		} else {
			dir, err := j.OutFolder(base + "_拆分")
			if err != nil {
				return conv.Fail("没法创建输出文件夹", err.Error())
			}
			used := map[string]int{}
			for _, s := range spans {
				name := conv.SafeName(s.name(base))
				if used[name]++; used[name] > 1 { // 同一段写了两次
					name += fmt.Sprintf(" (%d)", used[name])
				}
				paths = append(paths, filepath.Join(dir, name+".pdf"))
			}
		}
		return withRepair(ctx, j, in, pw, func(src, pw string) error {
			return splitPdfcpu(ctx, j, src, pw, spans, paths)
		})
	})
}

func splitPdfcpu(c context.Context, j *conv.Job, in, pw string, spans []span, paths []string) error {
	j.Report(0, "正在读取文件")
	ctx, err := readCtx(c, in, newConf(pw, model.SPLIT))
	if err != nil {
		return err
	}
	for k, s := range spans {
		if err := cancelled(c); err != nil {
			return err
		}
		j.Report(float64(k)/float64(len(spans)), fmt.Sprintf("第 %d/%d 个文件", k+1, len(spans)))
		nrs := make([]int, 0, s.to-s.from+1)
		for p := s.from; p <= s.to; p++ {
			nrs = append(nrs, p)
		}
		sub, err := pdfcpu.ExtractPages(c, ctx, nrs, false)
		if err != nil {
			return err
		}
		if err := writeFile(paths[k], func(w io.Writer) error { return api.WriteContext(c, sub, w) }); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- 旋转

// Rotate 把选中的页（pages 选项，默认全部）顺时针旋转 angle 度（90 | 180 | 270）
func Rotate() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		angle := ((j.Opt.Int(conv.OptAngle, 90) % 360) + 360) % 360
		if angle%90 != 0 {
			return conv.Fail("旋转角度只能是 90、180 或 270 度", "")
		}
		in := j.Input()
		pw := j.Opt.Str(conv.OptPassword, "")
		n, err := PageCount(in, pw)
		if err != nil {
			return err
		}
		rs, err := conv.ParseRanges(j.Opt.Str(conv.OptPages, ""), n)
		if err != nil {
			return err
		}
		out := j.OutFileNamed(j.Base()+"_旋转", ".pdf")
		if angle == 0 {
			return copyFile(in, out)
		}
		var sel []string
		for _, r := range rs {
			sel = append(sel, fmt.Sprintf("%d-%d", r.From, r.To))
		}
		j.Report(-1, "正在旋转")
		return withRepair(ctx, j, in, pw, func(src, pw string) error {
			f, err := os.Open(src)
			if err != nil {
				return conv.Fail("读不了这个文件", err.Error())
			}
			defer f.Close()
			return writeFile(out, func(w io.Writer) error {
				return api.Rotate(ctx, f, w, angle, sel, newConf(pw, model.ROTATE))
			})
		})
	})
}

// ---------------------------------------------------------------- 加密、解密

// Encrypt 给 PDF 加上打开密码（AES-256，所有者密码和打开密码相同）
func Encrypt() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		pw := normPassword(j.Opt.Str(conv.OptPassword, ""))
		if pw == "" {
			return conv.Fail("请先填上要设置的密码", "")
		}
		in := j.Input()
		out := j.OutFileNamed(j.Base()+"_加密", ".pdf")
		j.Report(-1, "正在加密")
		enc := func(src string) error {
			f, err := os.Open(src)
			if err != nil {
				return conv.Fail("读不了这个文件", err.Error())
			}
			defer f.Close()
			conf := newConf(pw, model.ENCRYPT)
			conf.EncryptUsingAES = true
			conf.EncryptKeyLength = 256
			conf.Permissions = model.PermissionsAll
			return writeFile(out, func(w io.Writer) error { return api.Encrypt(ctx, f, w, conf) })
		}
		err := safe(func() error { return enc(in) })
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return conv.ErrCancelled
		}
		if isUserErr(err) || pdfiumAvailable() != nil {
			if errors.Is(err, pdfcpu.ErrEncrypted) {
				return errAlreadyEncrypted
			}
			return pdfcpuErr(err, "")
		}
		// 已经加过密（比如只限制了编辑）或者 pdfcpu 读不了：先用 pdfium 去掉旧的加密、理顺结构
		fixed, ferr := repairedCopy(j, in, "")
		if ferr != nil {
			if ferr == errNeedPassword {
				return errAlreadyEncrypted
			}
			return ferr
		}
		if err := safe(func() error { return enc(fixed) }); err != nil {
			if ctx.Err() != nil {
				return conv.ErrCancelled
			}
			return pdfcpuErr(err, "")
		}
		return nil
	})
}

var errAlreadyEncrypted = conv.Fail("这个 PDF 已经有打开密码了，请先用「解除密码」去掉旧密码再加密", "")

// Decrypt 去掉 PDF 的打开密码和编辑、打印限制。password 选项填打开密码（只有编辑限制的文件可以不填）。
func Decrypt() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		pw := j.Opt.Str(conv.OptPassword, "")
		in := j.Input()
		out := j.OutFileNamed(j.Base()+"_解密", ".pdf")
		j.Report(-1, "正在解除密码")
		dec := func(src, pw string) error {
			f, err := os.Open(src)
			if err != nil {
				return conv.Fail("读不了这个文件", err.Error())
			}
			defer f.Close()
			return writeFile(out, func(w io.Writer) error { return api.Decrypt(ctx, f, w, newConf(pw, model.DECRYPT)) })
		}
		err := safe(func() error { return dec(in, pw) })
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return conv.ErrCancelled
		}
		if errors.Is(err, pdfcpu.ErrNotEncrypted) || isUserErr(err) || pdfiumAvailable() != nil {
			return pdfcpuErr(err, pw)
		}
		// pdfcpu 解不了（加密方式特殊、文件不规范……）：pdfium 能打开就能另存成不加密的
		d, oerr := Open(in, pw)
		if oerr != nil {
			return oerr
		}
		defer d.Close()
		if !d.Encrypted() {
			return pdfcpuErr(pdfcpu.ErrNotEncrypted, pw)
		}
		return d.SaveCopy(out)
	})
}
