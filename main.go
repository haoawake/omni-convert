// 万能格式转换：图片、视频、音频、Word / Excel / PPT、PDF 之间的各种格式转换，
// 还能改尺寸、裁成固定像素、压缩到指定大小、合并拆分 PDF。原生 Windows 程序，全部在本机完成。
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// 发版时由 -ldflags "-X main.version=x.y.z" 写入
var version = "dev"

const (
	appName = "万能格式转换"
	repoURL = "https://github.com/haoawake/omni-convert"
)

func init() {
	// 窗口和它的消息循环必须一直待在同一个系统线程上
	runtime.LockOSThread()
}

func main() {
	// 命令行模式：万能格式转换.exe -to img:jpg [-o 输出文件夹] [-set 键=值 …] 文件…
	if isCLI(os.Args[1:]) {
		os.Exit(runCLI(os.Args[1:]))
	}
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.StringVar(&shotFile, "shot", "", "")
	flag.StringVar(&shotSteps, "steps", "", "")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	files := flag.Args()
	if forwardToRunning(files) {
		return
	}
	runApp(files)
}

// isCLI 参数里出现命令行模式的选项（-to、-o、-set、-q、-list、-h，也可以写成 --to、-to=xx）时不开窗口
func isCLI(args []string) bool {
	for _, a := range args {
		if a == "/?" {
			return true
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "to", "o", "set", "q", "list", "h", "help":
			return true
		}
	}
	return false
}

func humanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / 1024
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	switch {
	case f < 10:
		return fmt.Sprintf("%.2f %s", f, units[i])
	case f < 100:
		return fmt.Sprintf("%.1f %s", f, units[i])
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}

// formatCount 给数字加千分位：1636235 → 1,636,235
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
