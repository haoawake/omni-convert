package main

// 命令行模式：不开窗口，直接转换。也用来做端到端测试。
//
//	万能格式转换.exe -to img:jpg [-o 输出文件夹] [-set 键=值 …] 文件…
//	万能格式转换.exe -list

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func runCLI(args []string) int {
	attachConsole()
	fs := flag.NewFlagSet("cli", flag.ContinueOnError)
	to := fs.String("to", "", "转换目标，比如 img:jpg、vid:mp4、pdf:merge（用 -list 查看全部）")
	out := fs.String("o", "", "输出文件夹（默认和原文件放在一起）")
	list := fs.Bool("list", false, "列出所有转换目标和它们的选项")
	quiet := fs.Bool("q", false, "不显示进度")
	var sets multiFlag
	fs.Var(&sets, "set", "选项，格式 键=值，可以写多次")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *list {
		printTargets()
		return 0
	}
	t := catalog.ByID(*to)
	if t == nil {
		fmt.Fprintf(os.Stderr, "不认识的转换目标 %q，用 -list 查看全部\n", *to)
		return 2
	}
	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "没有要转换的文件")
		return 2
	}
	ui := conv.Options{}
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "选项 %q 要写成 键=值\n", s)
			return 2
		}
		ui[k] = v
	}
	opt := t.Prepare(ui)
	// 命令行里直接写的转换器选项（比如 size=102400）也要生效，即使界面上没有对应的控件
	for k, v := range ui {
		switch k {
		case "sizepreset", "sizenum", "sizeunit", "res", "preset":
			continue // 界面专用的键，Prepare 已经换算过了
		}
		if opt[k] == "" {
			opt[k] = v
		}
	}
	if msg := t.Check(opt); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 2
	}
	defer catalog.Shutdown()

	var mu sync.Mutex
	done := make(chan *conv.Task, len(files))
	var runner *conv.Runner
	runner = conv.NewRunner(map[string]int{"image": 4, "video": 2, "audio": 3, "office": 1, "pdf": 2}, func(task *conv.Task) {
		info := runner.Snapshot(task)
		mu.Lock()
		defer mu.Unlock()
		name := filepath.Base(task.Job.Input())
		switch info.State {
		case conv.Running:
			if !*quiet {
				pct := "…"
				if info.Progress.Frac >= 0 {
					pct = fmt.Sprintf("%3.0f%%", info.Progress.Frac*100)
				}
				fmt.Printf("  %s %s %s\n", pct, name, info.Progress.Note)
			}
		case conv.Done, conv.Failed, conv.Cancelled:
			done <- task
		}
	})
	outDir := func(in string) string {
		if *out != "" {
			os.MkdirAll(*out, 0o755)
			return *out
		}
		return filepath.Dir(in)
	}
	var tasks []*conv.Task
	var abs []string
	for _, f := range files {
		p, err := filepath.Abs(f)
		if err != nil {
			p = f
		}
		abs = append(abs, p)
	}
	if t.Batch {
		tasks = append(tasks, &conv.Task{Job: conv.NewJob(abs, t.ID, opt, outDir(abs[0]), nil), Lane: t.Lane, Run: t.Run})
	} else {
		for _, p := range abs {
			tasks = append(tasks, &conv.Task{Job: conv.NewJob([]string{p}, t.ID, opt.Clone(), outDir(p), nil), Lane: t.Lane, Run: t.Run})
		}
	}
	start := time.Now()
	for _, task := range tasks {
		runner.Submit(task)
	}
	failed := 0
	for range tasks {
		task := <-done
		info := runner.Snapshot(task)
		mu.Lock()
		name := filepath.Base(task.Job.Input())
		if info.State == conv.Done {
			fmt.Printf("完成 %s（%.1f 秒）\n", name, info.Elapsed.Seconds())
			for _, o := range info.Outputs {
				size := ""
				if fi, err := os.Stat(o); err == nil && !fi.IsDir() {
					size = "  " + humanSize(fi.Size())
				}
				fmt.Printf("  → %s%s\n", o, size)
			}
			if info.Progress.Note != "" {
				fmt.Printf("  %s\n", info.Progress.Note)
			}
		} else {
			failed++
			fmt.Printf("失败 %s：%v\n", name, info.Err)
			if ue, ok := info.Err.(*conv.UserError); ok && ue.Detail != "" {
				fmt.Printf("  详细：%s\n", ue.Detail)
			}
		}
		mu.Unlock()
	}
	fmt.Printf("共 %d 个任务，失败 %d 个，用时 %.1f 秒\n", len(tasks), failed, time.Since(start).Seconds())
	if failed > 0 {
		return 1
	}
	return 0
}

func printTargets() {
	for _, p := range catalog.Pages {
		fmt.Printf("\n%s\n", p.Title)
		for _, t := range p.Targets {
			var keys []string
			for _, r := range t.Rows {
				for _, f := range r.Fields {
					if f.Key != "" && f.Type != catalog.Static {
						keys = append(keys, f.Key)
					}
				}
			}
			sort.Strings(keys)
			fmt.Printf("  %-14s %-10s %s\n", t.ID, t.Label, t.Hint)
			if len(keys) > 0 {
				fmt.Printf("  %-14s 选项：%s\n", "", strings.Join(keys, " "))
			}
		}
	}
}
