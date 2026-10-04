//go:build windows

package office

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ole "github.com/go-ole/go-ole"

	"github.com/haoawake/omni-convert/internal/conv"
)

// 所有 COM 调用都在一个专用线程上做（单线程套间）。程序实例在连续的任务之间复用，
// 空闲一分钟后退出。卡住的实例只要是我们自己启动的，就可以直接结束进程。

var (
	idleQuit    = 60 * time.Second // 空闲多久后退出 Office
	cancelGrace = 4 * time.Second  // 取消后等多久再强制结束
	dialogGrace = 4 * time.Second  // 弹出的提示框停留多久算卡住
)

// comApp 是一个正在运行的 Office 程序
type comApp struct {
	kind    appKind
	prog    progInfo
	app     obj
	pid     uint32
	owned   bool   // 进程是我们启动的（可以退出、可以强制结束）
	version string // 比如 "16.0"
	saved   map[string]any
}

func (a *comApp) name() string { return a.prog.appName(a.kind) }

// comReq 是交给 COM 线程的一件事
type comReq struct {
	kind   appKind
	ctx    context.Context
	report func(float64, string)
	fn     func(a *comApp) error
	quit   bool
	done   chan error

	mu       sync.Mutex
	pid      uint32          // 正在用的、可以结束的进程
	spawnExe string          // 正在启动的程序
	before   map[uint32]bool // 启动前已经有的进程
	resil    resilState      // 启动前的崩溃记录（强制结束后用来清理）
	killed   atomic.Bool
}

func (r *comReq) setPID(pid uint32) {
	r.mu.Lock()
	r.pid = pid
	r.spawnExe, r.before = "", nil
	r.mu.Unlock()
}

// watchPIDs 是需要检查有没有弹出提示框的进程：正在用的实例，或者正在启动的新进程
// （启动时也可能弹「激活」「安全模式」之类的提示框）
func (r *comReq) watchPIDs() []uint32 {
	r.mu.Lock()
	pid, exe, before := r.pid, r.spawnExe, r.before
	r.mu.Unlock()
	if pid != 0 {
		return []uint32{pid}
	}
	var out []uint32
	if exe != "" {
		for p := range processes(exe) {
			if !before[p] {
				out = append(out, p)
			}
		}
	}
	return out
}

type comWorker struct {
	reqs chan *comReq
	apps [numApps]*comApp // 只在 COM 线程里用

	mu    sync.Mutex
	owned map[uint32]ownedProc // 我们启动的进程，Shutdown 时兜底用
}

// ownedProc 是我们启动的一个 Office 进程
type ownedProc struct {
	kind  appKind
	resil resilState // 启动前的崩溃记录
}

var (
	workerMu   sync.Mutex
	theWorker  *comWorker
	workerOnce sync.Once
)

func worker() *comWorker {
	workerOnce.Do(func() {
		w := &comWorker{reqs: make(chan *comReq), owned: map[uint32]ownedProc{}}
		go w.loop()
		workerMu.Lock()
		theWorker = w
		workerMu.Unlock()
	})
	return theWorker
}

func (w *comWorker) loop() {
	runtime.LockOSThread()
	ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED) // 已经初始化过时返回 S_FALSE，不用管
	idle := time.NewTimer(time.Hour)
	idle.Stop()
	pump := time.NewTicker(200 * time.Millisecond)
	for {
		select {
		case r := <-w.reqs:
			var err error
			if r.quit {
				w.quitAll()
			} else {
				err = w.serve(r)
			}
			r.done <- err
			if w.anyApp() {
				idle.Reset(idleQuit)
			}
		case <-idle.C:
			w.quitAll()
		case <-pump.C:
			pumpMessages()
		}
	}
}

func (w *comWorker) anyApp() bool {
	for _, a := range w.apps {
		if a != nil {
			return true
		}
	}
	return false
}

func (w *comWorker) serve(r *comReq) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("office 内部错误：%v", p)
		}
	}()
	for attempt := 0; ; attempt++ {
		if r.ctx.Err() != nil {
			return conv.ErrCancelled
		}
		a, err := w.ensure(r)
		if err != nil {
			return err
		}
		if a.owned {
			r.setPID(a.pid)
		} else {
			r.setPID(0)
		}
		err = r.fn(a)
		w.restoreShared(a)
		if err == nil {
			return nil
		}
		if isDead(err) || (a.owned && !processAlive(a.pid)) {
			w.drop(r.kind)
			// 用户把 Office 关掉了：换一个新的实例再试一次
			if attempt == 0 && !r.killed.Load() && r.ctx.Err() == nil {
				r.report(-1, a.name()+" 意外退出了，正在重试…")
				continue
			}
		}
		return err
	}
}

// ensure 拿到可用的程序实例：已有的先检查还活着，没有就启动一个
func (w *comWorker) ensure(r *comReq) (*comApp, error) {
	if a := w.apps[r.kind]; a != nil {
		_, err := a.app.str("Name")
		if err == nil {
			return a, nil
		}
		w.drop(r.kind)
	}
	prog := comProg(r.kind)
	if prog.progID == "" {
		return nil, errUnavailable
	}
	cleanResiliency()
	before := processes(prog.exe)
	if r.kind == appPPT && len(before) > 0 {
		r.report(-1, "正在连接 "+prog.appName(r.kind)+"…") // PowerPoint 只有一个实例，会用已经开着的那个
	} else {
		r.report(-1, "正在启动 "+prog.appName(r.kind)+"…")
	}
	resil := snapshotResiliency(r.kind)
	r.mu.Lock()
	r.spawnExe, r.before, r.resil = prog.exe, before, resil
	r.mu.Unlock()

	clsid, err := ole.ClassIDFrom(prog.progID)
	if err != nil {
		return nil, &unavailableError{prog.appName(r.kind), err}
	}
	unk, err := ole.CreateInstance(clsid, ole.IID_IUnknown)
	if err != nil {
		return nil, &unavailableError{prog.appName(r.kind), err}
	}
	disp, err := unk.QueryInterface(ole.IID_IDispatch)
	unk.Release()
	if err != nil {
		return nil, &unavailableError{prog.appName(r.kind), err}
	}
	a := &comApp{kind: r.kind, prog: prog, app: obj{disp}}
	switch r.kind {
	case appExcel:
		if h, err := a.app.int("Hwnd"); err == nil {
			a.pid = pidOfWindow(uintptr(h))
		}
	case appPPT:
		if h, err := a.app.int("HWND"); err == nil {
			a.pid = pidOfWindow(uintptr(h))
		}
	}
	if a.pid == 0 {
		var fresh []uint32
		for p := range processes(prog.exe) {
			if !before[p] {
				fresh = append(fresh, p)
			}
		}
		if len(fresh) == 1 {
			a.pid = fresh[0]
		}
	}
	a.owned = a.pid != 0 && !before[a.pid]
	if a.owned {
		w.mu.Lock()
		w.owned[a.pid] = ownedProc{r.kind, resil}
		w.mu.Unlock()
	}
	r.setPID(0)
	a.version, _ = a.app.str("Version")
	w.configure(a)
	w.apps[r.kind] = a
	return a, nil
}

// configure 关掉一切会弹窗、会运行宏、会刷新屏幕的东西
func (w *comWorker) configure(a *comApp) {
	o := a.app
	switch a.kind {
	case appWord:
		o.put("Visible", false)
		o.put("DisplayAlerts", 0) // wdAlertsNone
		o.put("ScreenUpdating", false)
		o.put("AutomationSecurity", 3) // msoAutomationSecurityForceDisable
	case appExcel:
		o.put("Visible", false)
		o.put("DisplayAlerts", false)
		o.put("ScreenUpdating", false)
		o.put("EnableEvents", false)
		o.put("AskToUpdateLinks", false)
		o.put("AutomationSecurity", 3)
	case appPPT:
		// PowerPoint 只有一个实例，可能是用户正在用的：改过的设置用完要还原。
		// 注意 PowerPoint 不能设置 Visible=false（会报错）。
		if !a.owned {
			a.saved = map[string]any{}
			if v, err := o.int("DisplayAlerts"); err == nil {
				a.saved["DisplayAlerts"] = v
			}
			if v, err := o.int("AutomationSecurity"); err == nil {
				a.saved["AutomationSecurity"] = v
			}
		}
		o.put("DisplayAlerts", 1) // ppAlertsNone
		o.put("AutomationSecurity", 3)
	}
}

// restoreShared 用户自己的 PowerPoint：每次用完把设置还原，下次用时再改
func (w *comWorker) restoreShared(a *comApp) {
	if a.owned || a.kind != appPPT || w.apps[a.kind] != a {
		return
	}
	for k, v := range a.saved {
		a.app.put(k, v)
	}
	w.apps[a.kind] = nil // 下次重新连接、重新保存设置
	a.app.release()
}

func (w *comWorker) drop(kind appKind) {
	if a := w.apps[kind]; a != nil {
		w.apps[kind] = nil
		a.app.release()
	}
}

func countOf(o obj, coll string) int {
	c, err := o.sub(coll)
	if err != nil || !c.ok() {
		return 0
	}
	defer c.release()
	n, _ := c.int("Count")
	return n
}

// quitAll 退出所有我们启动的 Office，并等它们的进程真正结束
func (w *comWorker) quitAll() {
	var pids []uint32
	for k := range w.apps {
		if pid := w.quit(appKind(k)); pid != 0 {
			pids = append(pids, pid)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		if !waitExit(pid, time.Until(deadline)) && !userVisible(pid) {
			w.kill(pid)
		}
		w.disown(pid)
	}
	cleanResiliency()
}

// kill 强制结束我们启动的进程，并清理它留下的崩溃记录
func (w *comWorker) kill(pid uint32) {
	w.mu.Lock()
	op, ok := w.owned[pid]
	w.mu.Unlock()
	killPID(pid)
	if ok {
		restoreResiliency(op.kind, op.resil)
	}
}

// quit 退出一个程序，返回需要等它结束的进程号
func (w *comWorker) quit(kind appKind) uint32 {
	a := w.apps[kind]
	if a == nil {
		return 0
	}
	w.apps[kind] = nil
	defer a.app.release()
	o := a.app
	switch kind {
	case appWord:
		if countOf(o, "Documents") > 0 { // 用户打开的文档跑进了我们的实例：交给用户
			o.put("ScreenUpdating", true)
			o.put("DisplayAlerts", -1)
			o.put("Visible", true)
			w.disown(a.pid)
			return 0
		}
		o.call("Quit", 0) // wdDoNotSaveChanges
	case appExcel:
		if countOf(o, "Workbooks") > 0 {
			o.put("ScreenUpdating", true)
			o.put("DisplayAlerts", true)
			o.put("EnableEvents", true)
			o.put("Visible", true)
			o.put("UserControl", true)
			w.disown(a.pid)
			return 0
		}
		o.call("Quit")
	case appPPT:
		if !a.owned || countOf(o, "Presentations") > 0 { // 用户也在用这个 PowerPoint：不退出
			w.disown(a.pid)
			return 0
		}
		o.call("Quit")
	}
	if a.owned {
		return a.pid
	}
	return 0
}

func (w *comWorker) disown(pid uint32) {
	w.mu.Lock()
	delete(w.owned, pid)
	w.mu.Unlock()
}

// killReq 强制结束一个卡住的请求用的 Office 进程（只结束我们自己启动、用户也没在用的）
func (w *comWorker) killReq(r *comReq) {
	r.killed.Store(true)
	r.mu.Lock()
	pid, exe, before, resil := r.pid, r.spawnExe, r.before, r.resil
	r.mu.Unlock()
	if pid != 0 && !userVisible(pid) {
		w.kill(pid)
	}
	if exe != "" {
		killed := false
		for p := range processes(exe) {
			if !before[p] && !userVisible(p) {
				killPID(p)
				killed = true
			}
		}
		if killed {
			restoreResiliency(r.kind, resil)
		}
	}
	cleanResiliency()
}

// userVisible 进程有用户能看见的主窗口：说明用户在用它，不能结束
func userVisible(pid uint32) bool {
	for _, h := range enumWindows(0, pid) {
		switch windowClass(h) {
		case "OpusApp", "XLMAIN", "PPTFrameClass":
			if v, _, _ := procIsWindowVisible.Call(h); v != 0 {
				return true
			}
		}
	}
	return false
}

// runCOM 把 fn 交给 COM 线程执行，同时负责超时、取消和「弹出提示框卡住」的处理
func runCOM(t *task, kind appKind, timeout time.Duration, fn func(a *comApp) error) error {
	w := worker()
	r := &comReq{kind: kind, ctx: t.ctx, report: t.report, fn: fn, done: make(chan error, 1)}
	select {
	case w.reqs <- r:
	case <-t.ctx.Done():
		return conv.ErrCancelled
	}
	name := comProg(kind).appName(kind)
	wait := func(d time.Duration) {
		select {
		case <-r.done:
		case <-time.After(d):
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var dialogSince time.Time
	for {
		select {
		case err := <-r.done:
			if t.ctx.Err() != nil {
				return conv.ErrCancelled
			}
			return err
		case <-t.ctx.Done():
			select {
			case <-r.done:
				return conv.ErrCancelled
			case <-time.After(cancelGrace):
			}
			w.killReq(r)
			wait(30 * time.Second)
			return conv.ErrCancelled
		case <-deadline.C:
			w.killReq(r)
			wait(30 * time.Second)
			return conv.Fail(name+" 转换超时了", fmt.Sprintf("超过 %d 分钟没有完成，可能文件太大或者 %s 卡住了", int(timeout.Minutes()), name))
		case <-tick.C:
			var d []string
			for _, pid := range r.watchPIDs() {
				d = append(d, dialogsOf(pid)...)
			}
			if len(d) == 0 {
				dialogSince = time.Time{}
				continue
			}
			if dialogSince.IsZero() {
				dialogSince = time.Now()
			}
			if time.Since(dialogSince) >= dialogGrace {
				w.killReq(r)
				wait(30 * time.Second)
				return conv.Fail(name+" 弹出了一个提示框，没法自动转换", strings.Join(d, "；"))
			}
		}
	}
}

// shutdownCOM 退出所有我们启动的 Office
func shutdownCOM() {
	workerMu.Lock()
	w := theWorker
	workerMu.Unlock()
	if w == nil {
		return
	}
	r := &comReq{quit: true, ctx: context.Background(), report: func(float64, string) {}, done: make(chan error, 1)}
	select {
	case w.reqs <- r:
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
		}
	case <-time.After(30 * time.Second):
	}
	// 兜底：还活着的就强制结束
	w.mu.Lock()
	var left []uint32
	for pid := range w.owned {
		left = append(left, pid)
	}
	w.mu.Unlock()
	for _, pid := range left {
		if processAlive(pid) && !userVisible(pid) {
			w.kill(pid)
		}
		w.disown(pid)
	}
}
