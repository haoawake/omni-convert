//go:build stub

package catalog

// 开发界面时用（go build -tags stub）：不连接真正的转换器，每个任务假装转换两秒

import (
	"context"
	"os"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
)

func DetectGPU(done func()) {
	if done != nil {
		go done()
	}
}

func OfficeInfo() string { return "Microsoft Office" }

func Shutdown() {}

func bindAll() {
	for _, p := range Pages {
		for _, t := range p.Targets {
			t.Run = fake
		}
	}
}

func fake(ctx context.Context, j *conv.Job) error {
	for i := 0; i < 20; i++ {
		select {
		case <-ctx.Done():
			return conv.ErrCancelled
		case <-time.After(100 * time.Millisecond):
		}
		j.Report(float64(i)/20, "")
	}
	if conv.Ext(j.Input()) == ".gif" {
		return conv.Fail("假装失败了", "细节")
	}
	out := j.OutFile(".txt")
	return os.WriteFile(out, []byte("stub"), 0o644)
}
