// Package tools 找到随程序一起发布的转换引擎（ffmpeg、ImageMagick、pdfium），并在后台运行它们。
//
// Windows 发布包里的目录结构（macOS 的「万能格式转换.app」见 tools_unix.go）：
//
//	万能格式转换.exe
//	tools\ffmpeg\ffmpeg.exe、ffprobe.exe 和一堆 dll
//	tools\magick\magick.exe 和配置文件
//	tools\pdfium\pdfium.dll
//
// 开发时（go run / go test）会从当前目录一路往上找 tools 文件夹；也可以用环境变量 OMNI_TOOLS 指定。
package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/haoawake/omni-convert/internal/conv"
)

var (
	rootOnce sync.Once
	root     string
)

// Root 是 tools 文件夹的位置，找不到时返回空字符串
func Root() string {
	rootOnce.Do(func() { root = findRoot() })
	return root
}

func findRoot() string {
	if d := os.Getenv("OMNI_TOOLS"); d != "" {
		return d
	}
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, s := range starts {
		for d := s; ; {
			if fi, err := os.Stat(filepath.Join(d, "tools", "ffmpeg")); err == nil && fi.IsDir() {
				return filepath.Join(d, "tools")
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return ""
}

func path(parts ...string) string {
	r := Root()
	if r == "" {
		r = "tools"
	}
	return filepath.Join(append([]string{r}, parts...)...)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Need 检查某个引擎在不在，不在时返回给用户看的错误
func Need(file string) error {
	if _, err := os.Stat(file); err != nil {
		name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		return conv.Fail("缺少转换组件 "+name, missingHint)
	}
	return nil
}

// isMagick 判断是不是 ImageMagick 的主程序
func isMagick(exe string) bool {
	b := filepath.Base(exe)
	return strings.EqualFold(b, "magick.exe") || b == "magick"
}

// Command 准备运行一个外部程序：不弹黑色窗口，ImageMagick 只用自带的配置
func Command(ctx context.Context, exe string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	if isMagick(exe) {
		cmd.Env = append(os.Environ(), magickEnv(exe)...)
	}
	cmd.WaitDelay = 0
	return cmd
}

// RunOptions 是 Run 的可选参数
type RunOptions struct {
	Stdin    io.Reader
	OnStdout func(line string) // 每读到一行标准输出调用一次（ffmpeg -progress pipe:1 用）
	OnStderr func(line string)
	Dir      string
}

// Run 运行外部程序直到结束，返回标准输出。失败时的错误里带上标准错误输出的最后几行。
// ctx 取消时会立刻结束进程，返回 conv.ErrCancelled。
func Run(ctx context.Context, exe string, args []string, ro *RunOptions) ([]byte, error) {
	if err := Need(exe); err != nil {
		return nil, err
	}
	if ro == nil {
		ro = &RunOptions{}
	}
	cmd := Command(ctx, exe, args...)
	cmd.Stdin = ro.Stdin
	cmd.Dir = ro.Dir

	var stdout bytes.Buffer
	tail := &tailBuffer{max: 30}
	var wg sync.WaitGroup
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("没法启动 %s：%w", filepath.Base(exe), err)
	}
	trackProcess(cmd.Process)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if ro.OnStdout == nil {
			io.Copy(&stdout, outPipe)
			return
		}
		sc := bufio.NewScanner(io.TeeReader(outPipe, &stdout))
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			ro.OnStdout(sc.Text())
		}
		io.Copy(io.Discard, outPipe)
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(errPipe)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			line := sc.Text()
			tail.add(line)
			if ro.OnStderr != nil {
				ro.OnStderr(line)
			}
		}
		io.Copy(io.Discard, errPipe)
	}()
	wg.Wait()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return stdout.Bytes(), conv.ErrCancelled
	}
	if err != nil {
		return stdout.Bytes(), &ToolError{Tool: strings.TrimSuffix(filepath.Base(exe), ".exe"), Err: err, Stderr: tail.lines()}
	}
	return stdout.Bytes(), nil
}

// ToolError 是外部程序运行失败
type ToolError struct {
	Tool   string
	Err    error
	Stderr []string // 标准错误输出的最后几行
}

func (e *ToolError) Error() string {
	last := e.LastLine()
	if last == "" {
		return fmt.Sprintf("%s 运行失败（%v）", e.Tool, e.Err)
	}
	return fmt.Sprintf("%s 运行失败：%s", e.Tool, last)
}

// LastLine 是最后一行有意义的错误输出
func (e *ToolError) LastLine() string {
	for i := len(e.Stderr) - 1; i >= 0; i-- {
		l := strings.TrimSpace(e.Stderr[i])
		if l == "" || strings.HasPrefix(l, "Conversion failed") || strings.HasPrefix(l, "[out#") && strings.Contains(l, "Nothing was written") {
			continue
		}
		return l
	}
	return ""
}

// AsToolError 取出错误链里的 ToolError
func AsToolError(err error) (*ToolError, bool) {
	var te *ToolError
	ok := errors.As(err, &te)
	return te, ok
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []string
}

func (t *tailBuffer) add(s string) {
	t.mu.Lock()
	t.buf = append(t.buf, s)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	t.mu.Unlock()
}

func (t *tailBuffer) lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.buf...)
}

// scanLinesCR 按 \n 或 \r 分行（ffmpeg 的进度行用 \r 结尾）
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
