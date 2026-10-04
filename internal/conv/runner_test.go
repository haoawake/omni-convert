package conv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func touch(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "x.jpg")
	os.WriteFile(p, []byte("x"), 0o644)
	return p
}

func TestRunnerLanesAndCancel(t *testing.T) {
	in := touch(t)
	var mu sync.Mutex
	var peak, cur int32
	done := make(chan *Task, 16)
	r := NewRunner(map[string]int{"a": 2}, func(t *Task) {
		mu.Lock()
		defer mu.Unlock()
	})
	r.onUpdate = func(t *Task) {
		if s := r.Snapshot(t).State; s == Done || s == Failed || s == Cancelled {
			done <- t
		}
	}
	slow := func(ctx context.Context, j *Job) error {
		n := atomic.AddInt32(&cur, 1)
		defer atomic.AddInt32(&cur, -1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		select {
		case <-time.After(50 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ErrCancelled
		}
	}
	var tasks []*Task
	for i := 0; i < 5; i++ {
		tk := &Task{Job: NewJob([]string{in}, "t", nil, t.TempDir(), nil), Lane: "a", Run: slow}
		tasks = append(tasks, tk)
		r.Submit(tk)
	}
	for range tasks {
		<-done
	}
	if peak != 2 {
		t.Fatalf("同一车道最多同时跑 2 个，实际 %d", peak)
	}
	for _, tk := range tasks {
		if r.Snapshot(tk).State != Done {
			t.Fatal("应该都完成了")
		}
	}

	// 取消：运行中的停下，排队的直接移出
	block := func(ctx context.Context, j *Job) error { <-ctx.Done(); return ctx.Err() }
	var bt []*Task
	for i := 0; i < 4; i++ {
		tk := &Task{Job: NewJob([]string{in}, "t", nil, t.TempDir(), nil), Lane: "a", Run: block}
		bt = append(bt, tk)
		r.Submit(tk)
	}
	time.Sleep(30 * time.Millisecond)
	r.CancelAll()
	for range bt {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("取消后任务没有结束")
		}
	}
	for _, tk := range bt {
		if s := r.Snapshot(tk).State; s != Cancelled {
			t.Fatalf("状态应该是已取消，实际 %v", s)
		}
	}
	if r.Busy() != 0 {
		t.Fatal("队列应该空了")
	}

	// 转换器崩溃不能带崩整个程序
	boom := &Task{Job: NewJob([]string{in}, "t", nil, t.TempDir(), nil), Lane: "b",
		Run: func(ctx context.Context, j *Job) error { panic("坏了") }}
	r.Submit(boom)
	<-done
	info := r.Snapshot(boom)
	var ue *UserError
	if info.State != Failed || !errors.As(info.Err, &ue) {
		t.Fatalf("崩溃应该变成失败：%v %v", info.State, info.Err)
	}
}

func TestRunnerMissingInput(t *testing.T) {
	done := make(chan *Task, 1)
	var r *Runner
	r = NewRunner(nil, func(tk *Task) {
		if s := r.Snapshot(tk).State; s == Failed || s == Done {
			done <- tk
		}
	})
	ran := false
	tk := &Task{Job: NewJob([]string{filepath.Join(t.TempDir(), "不存在.jpg")}, "t", nil, t.TempDir(), nil), Lane: "a",
		Run: func(ctx context.Context, j *Job) error { ran = true; return nil }}
	r.Submit(tk)
	<-done
	if info := r.Snapshot(tk); info.State != Failed || ran {
		t.Fatalf("找不到输入文件应该直接失败，不运行转换器：%v", info.State)
	}
}
