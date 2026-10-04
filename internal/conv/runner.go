package conv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"
)

// State 是任务的状态
type State int

const (
	Waiting State = iota
	Running
	Done
	Failed
	Cancelled
)

// Task 是排进队列的一个任务
type Task struct {
	Job  *Job
	Lane string // 在哪条「车道」排队：同一车道里同时运行的任务数有上限
	Run  RunFunc

	// 以下字段由 Runner 维护，读的时候用 Snapshot
	state    State
	progress Progress
	err      error
	started  time.Time
	ended    time.Time
	cancel   context.CancelFunc
	lastNote time.Time
	Tag      any // 调用者自己用（界面上对应的那一行）
}

// TaskInfo 是任务某一时刻的状态
type TaskInfo struct {
	State    State
	Progress Progress
	Err      error
	Elapsed  time.Duration
	Outputs  []string
}

// Runner 按车道排队执行任务
type Runner struct {
	mu       sync.Mutex
	lanes    map[string]*lane
	limits   map[string]int
	running  map[*Task]bool
	onUpdate func(*Task)
}

type lane struct {
	queue   []*Task
	running int
	limit   int
}

// NewRunner 创建执行器。limits 是每条车道同时运行的任务数（没列出的车道是 1）。
// onUpdate 在任务状态或进度变化时调用（在工作 goroutine 里，要自己切回界面线程）。
func NewRunner(limits map[string]int, onUpdate func(*Task)) *Runner {
	return &Runner{lanes: map[string]*lane{}, limits: limits, running: map[*Task]bool{}, onUpdate: onUpdate}
}

func (r *Runner) laneOf(name string) *lane {
	l := r.lanes[name]
	if l == nil {
		l = &lane{limit: max(r.limits[name], 1)}
		r.lanes[name] = l
	}
	return l
}

// Submit 把任务排进队列
func (r *Runner) Submit(t *Task) {
	r.mu.Lock()
	t.state = Waiting
	t.err = nil
	t.progress = Progress{}
	if t.Job != nil {
		t.Job.progress = func(p Progress) { r.report(t, p) }
	}
	l := r.laneOf(t.Lane)
	l.queue = append(l.queue, t)
	r.pump(l)
	// 已经开始运行的任务由 exec 自己通知（它可能已经结束了，这里再通知会重复）
	queued := t.state == Waiting
	r.mu.Unlock()
	if queued {
		r.notify(t)
	}
}

// pump 在有空位时启动排队的任务，调用时要持有锁
func (r *Runner) pump(l *lane) {
	for l.running < l.limit && len(l.queue) > 0 {
		t := l.queue[0]
		l.queue = l.queue[1:]
		l.running++
		ctx, cancel := context.WithCancel(context.Background())
		t.cancel = cancel
		t.state = Running
		t.started = time.Now()
		r.running[t] = true
		go r.exec(ctx, l, t)
	}
}

func (r *Runner) exec(ctx context.Context, l *lane, t *Task) {
	r.notify(t)
	err := safeRun(ctx, t)
	if err == nil && ctx.Err() != nil {
		err = ErrCancelled
	}
	t.Job.Finish(err != nil)

	r.mu.Lock()
	t.ended = time.Now()
	t.cancel = nil
	delete(r.running, t)
	switch {
	case err == nil:
		t.state = Done
		t.progress.Frac = 1
	case errors.Is(err, ErrCancelled) || ctx.Err() != nil:
		t.state = Cancelled
	default:
		t.state = Failed
		t.err = err
	}
	l.running--
	r.pump(l)
	r.mu.Unlock()
	r.notify(t)
}

// safeRun 运行转换器；转换器里的意外崩溃变成一个错误，不让整个程序退出
func safeRun(ctx context.Context, t *Task) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Fail("程序内部出错了，这个文件没转成", fmt.Sprintf("%v\n%s", p, debug.Stack()))
		}
	}()
	if t.Run == nil {
		return Fail("还不支持这种转换", "")
	}
	for _, in := range t.Job.Inputs {
		if _, err := os.Stat(in); err != nil {
			return Fail("找不到这个文件，可能被移动或删除了", in)
		}
	}
	return t.Run(ctx, t.Job)
}

func (r *Runner) report(t *Task, p Progress) {
	r.mu.Lock()
	if t.state != Running {
		r.mu.Unlock()
		return
	}
	// 进度可能来得很密（PDF 一页一页渲染），最多每 80 毫秒刷新一次界面
	now := time.Now()
	changedNote := p.Note != t.progress.Note
	t.progress = p
	if !changedNote && now.Sub(t.lastNote) < 80*time.Millisecond {
		r.mu.Unlock()
		return
	}
	t.lastNote = now
	r.mu.Unlock()
	r.notify(t)
}

func (r *Runner) notify(t *Task) {
	if r.onUpdate != nil {
		r.onUpdate(t)
	}
}

// Cancel 取消一个任务：还在排队的直接移出队列，正在运行的通知它停下
func (r *Runner) Cancel(t *Task) {
	r.mu.Lock()
	if t.state == Waiting {
		for _, l := range r.lanes {
			for i, q := range l.queue {
				if q == t {
					l.queue = append(l.queue[:i:i], l.queue[i+1:]...)
					t.state = Cancelled
					r.mu.Unlock()
					r.notify(t)
					return
				}
			}
		}
	}
	if t.state == Running && t.cancel != nil {
		t.cancel()
	}
	r.mu.Unlock()
}

// CancelAll 取消所有排队和运行中的任务
func (r *Runner) CancelAll() {
	r.mu.Lock()
	var dropped []*Task
	for _, l := range r.lanes {
		for _, t := range l.queue {
			t.state = Cancelled
			dropped = append(dropped, t)
		}
		l.queue = nil
	}
	for t := range r.running {
		if t.cancel != nil {
			t.cancel()
		}
	}
	r.mu.Unlock()
	for _, t := range dropped {
		r.notify(t)
	}
}

// Busy 返回还没结束的任务数（排队 + 运行）
func (r *Runner) Busy() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lanes {
		n += len(l.queue) + l.running
	}
	return n
}

// Snapshot 读取任务当前的状态
func (r *Runner) Snapshot(t *Task) TaskInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := TaskInfo{State: t.state, Progress: t.progress, Err: t.err}
	switch t.state {
	case Running:
		info.Elapsed = time.Since(t.started)
	case Done, Failed, Cancelled:
		if !t.started.IsZero() {
			info.Elapsed = t.ended.Sub(t.started)
		}
	}
	if t.state == Done && t.Job != nil {
		info.Outputs = t.Job.Outputs()
	}
	return info
}
