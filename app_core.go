package main

// 两个平台的界面共用的部分：每一页的文件列表和设置、把文件分到对应的页、「开始转换」要转哪些文件、
// 每一行显示什么、一批任务结束后的总结、记住上次的设置。
// 怎么画、怎么弹窗由 Windows（ui.go 等）和 macOS（ui_darwin.go）各自实现。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haoawake/omni-convert/internal/catalog"
	"github.com/haoawake/omni-convert/internal/conv"
)

// item 是文件列表里的一行
type item struct {
	path    string
	size    int64
	task    *conv.Task // 最近一次转换
	key     string     // 最近一次转换用的目标和选项，没变就不重复转
	outText string     // 转换结果的说明（完成后算一次）
}

// pageState 是左侧每一页各自的状态
type pageState struct {
	page   *catalog.Page
	target *catalog.Target
	opts   conv.Options
	items  []*item
}

// session 是点一次「开始转换」启动的一批任务
type session struct {
	tasks   []*conv.Task
	page    int
	started time.Time
}

// core 是界面的状态，和怎么显示无关
type core struct {
	pages  []*pageState
	cur    int
	runner *conv.Runner
	sess   *session

	outMode string // source = 和原文件放在一起；custom = 指定文件夹
	outDir  string
}

// initCore 建好每一页、读上次的设置、准备执行器。onUpdate 在任务状态变化时调用（在工作 goroutine 里）。
func (c *core) initCore(onUpdate func()) {
	c.outMode = "source"
	for _, p := range catalog.Pages {
		c.pages = append(c.pages, &pageState{page: p, target: p.Targets[0], opts: conv.Options{}})
	}
	// 默认目标：图片 JPG、视频 MP4、音频 MP3、文档 PDF、PDF 转 Word
	for i, id := range []string{"img:jpg", "vid:mp4", "aud:mp3", "doc:pdf", "pdf:docx"} {
		if t := catalog.ByID(id); t != nil {
			c.pages[i].target = t
		}
	}
	c.loadSettings()
	for _, ps := range c.pages {
		ps.target.Defaults(ps.opts)
	}
	c.runner = conv.NewRunner(map[string]int{
		"image": 4, "video": 2, "audio": 3, "office": 1, "pdf": 2,
	}, func(*conv.Task) { onUpdate() })
}

func (c *core) page() *pageState { return c.pages[c.cur] }

func (c *core) acceptCount(t *catalog.Target) int {
	n := 0
	for _, it := range c.page().items {
		if t.Accept(it.path) {
			n++
		}
	}
	return n
}

func (c *core) pageBusy(ps *pageState) bool {
	for _, it := range ps.items {
		if it.task != nil {
			if s := c.runner.Snapshot(it.task).State; s == conv.Waiting || s == conv.Running {
				return true
			}
		}
	}
	return false
}

// stopPage 停止当前页的所有任务（别的页的不受影响）
func (c *core) stopPage() {
	for _, it := range c.page().items {
		if it.task != nil {
			c.runner.Cancel(it.task)
		}
	}
}

// clearPage 清空当前页的列表（正在转换的先停下）
func (c *core) clearPage() {
	c.stopPage()
	c.page().items = nil
}

// ---------------------------------------------------------------- 添加文件

const maxFolderFiles = 5000

type foundFiles struct {
	files      []string
	tooMany    bool
	switchPage bool
}

// hasDir 判断这些路径里有没有文件夹（有的话要在后台展开）
func hasDir(paths []string) bool {
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// collectFiles 展开文件夹，找出里面所有认识的文件（跳过隐藏文件夹），最多 maxFolderFiles 个
func collectFiles(paths []string) ([]string, bool) {
	var files []string
	tooMany := false
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != p && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "$")) {
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := conv.KindOf(path); ok {
				if len(files) >= maxFolderFiles {
					tooMany = true
					return filepath.SkipAll
				}
				files = append(files, path)
			}
			return nil
		})
		if tooMany {
			break
		}
	}
	return files, tooMany
}

// addResolved 把一批文件（已经展开了文件夹）加进列表。能放进当前页的放当前页，其余的按类型放进对应的页；
// 如果当前页一个都放不了，就切到放得最多的那一页（switched 为 true）。返回要提示给用户的话。
func (c *core) addResolved(files []string, tooMany, switchPage bool) (msg string, isErr, switched bool) {
	seen := make([]map[string]bool, len(c.pages))
	for i, ps := range c.pages {
		seen[i] = make(map[string]bool, len(ps.items))
		for _, it := range ps.items {
			seen[i][strings.ToLower(it.path)] = true
		}
	}
	added := make([]int, len(c.pages))
	unknown, dup := 0, 0
	curFirst := false
	for _, f := range files {
		dest := -1
		if c.page().page.Accepts(f) {
			dest = c.cur
		} else if k, ok := conv.KindOf(f); ok {
			for i, ps := range c.pages {
				if ps.page.Kind == k {
					dest = i
				}
			}
		}
		if dest < 0 {
			unknown++
			continue
		}
		ps := c.pages[dest]
		if key := strings.ToLower(f); seen[dest][key] {
			dup++
			continue
		} else {
			seen[dest][key] = true
		}
		var size int64
		if fi, err := os.Stat(f); err == nil {
			size = fi.Size()
		}
		ps.items = append(ps.items, &item{path: f, size: size})
		added[dest]++
		if dest == c.cur {
			curFirst = true
		}
	}
	total := 0
	best := c.cur
	for i, n := range added {
		total += n
		if n > added[best] {
			best = i
		}
	}
	if switchPage && !curFirst && total > 0 {
		c.cur = best
		switched = true
	}
	var parts []string
	if total > 0 {
		m := "已添加 " + itoa(total) + " 个文件"
		var where []string
		for i, n := range added {
			if n > 0 && i != c.cur {
				where = append(where, c.pages[i].page.Kind.Name()+" "+itoa(n)+" 个")
			}
		}
		if len(where) > 0 {
			m += "（其中" + strings.Join(where, "、") + "放在对应的页里）"
		}
		parts = append(parts, m)
	}
	if dup > 0 {
		parts = append(parts, itoa(dup)+" 个已经在列表里了")
	}
	if unknown > 0 {
		parts = append(parts, itoa(unknown)+" 个文件不认识，跳过了")
	}
	if tooMany {
		parts = append(parts, "文件夹里文件太多，只加了前 "+itoa(maxFolderFiles)+" 个")
	}
	return strings.Join(parts, "，"), total == 0, switched
}

// moveItem 把当前页的第 i 行和第 j 行换个位置（合并 PDF 时决定顺序）
func (c *core) moveItem(i, j int) bool {
	ps := c.page()
	if i < 0 || j < 0 || i >= len(ps.items) || j >= len(ps.items) {
		return false
	}
	ps.items[i], ps.items[j] = ps.items[j], ps.items[i]
	return true
}

// removeItems 从当前页移除这几行（正在转换的先停下）。合并任务里的文件被移除时整个合并停下，stoppedMerge 为 true。
func (c *core) removeItems(sel []int) (stoppedMerge bool) {
	ps := c.page()
	drop := map[int]bool{}
	for _, i := range sel {
		if i < 0 || i >= len(ps.items) {
			continue
		}
		drop[i] = true
		if t := ps.items[i].task; t != nil {
			if s := c.runner.Snapshot(t).State; s == conv.Running || s == conv.Waiting {
				stoppedMerge = stoppedMerge || len(t.Job.Inputs) > 1
				c.runner.Cancel(t)
			}
		}
	}
	var keep []*item
	for i, it := range ps.items {
		if !drop[i] {
			keep = append(keep, it)
		}
	}
	ps.items = keep
	return stoppedMerge
}

// ---------------------------------------------------------------- 开始转换

type startKind int

const (
	startOK        startKind = iota
	startNeedFiles           // 列表是空的：打开「添加文件」
	startWarn                // 弹出提示框（msg、detail）
	startError               // 弹出错误框
	startToast               // 底栏上提示一句
)

type startResult struct {
	kind        startKind
	msg, detail string
}

func (c *core) runKey(t *catalog.Target, opt conv.Options) string {
	k := t.ID + "|" + opt.String() + "|" + c.outMode
	if c.outMode == "custom" {
		k += "|" + c.outDir
	}
	return k
}

// start 开始转换当前页列表里还没转过的文件；only 不为 nil 时只转这一个。
// 合并类的目标（合并 PDF、图片合成 PDF）总是把列表里所有能用的文件按当前顺序合成一个。
func (c *core) start(only *item) startResult {
	ps := c.page()
	t := ps.target
	if len(ps.items) == 0 {
		return startResult{kind: startNeedFiles}
	}
	opt := t.Prepare(ps.opts)
	if msg := t.Check(opt); msg != "" {
		return startResult{startWarn, msg, ""}
	}
	if c.outMode == "custom" {
		if err := os.MkdirAll(c.outDir, 0o755); err != nil || !writable(c.outDir) {
			return startResult{startError, "没法把结果保存到这个文件夹", c.outDir + "\n\n请在下面的「保存到」里换一个文件夹。"}
		}
	}
	key := c.runKey(t, opt)
	var accepted []*item
	for _, it := range ps.items {
		if t.Accept(it.path) && (only == nil || it == only || t.Batch) {
			accepted = append(accepted, it)
		}
	}
	if len(accepted) == 0 {
		return startResult{startWarn, "列表里没有能转成「" + t.Label + "」的文件", "换一个目标格式，或者添加别的文件。"}
	}
	busy := func(it *item) bool {
		if it.task == nil {
			return false
		}
		s := c.runner.Snapshot(it.task).State
		return s == conv.Waiting || s == conv.Running
	}
	done := func(it *item) bool {
		return it.task != nil && it.key == key && c.runner.Snapshot(it.task).State == conv.Done
	}
	var todo []*item
	if t.Batch {
		// 合并的结果取决于有哪些文件、什么顺序，都写进 key 里：加了文件、调了顺序就会重新合并
		var paths []string
		for _, it := range accepted {
			if busy(it) {
				return startResult{startToast, "正在合并，等它完成或者先停止", ""}
			}
			paths = append(paths, it.path)
		}
		key += "|" + strings.Join(paths, "\n")
		all := only == nil
		for _, it := range accepted {
			all = all && done(it)
		}
		if all && only == nil {
			return startResult{startToast, "这些文件已经用同样的设置合并过了；改了设置或顺序再点就会重新合并", ""}
		}
		todo = accepted
	} else {
		for _, it := range accepted {
			if !busy(it) && !done(it) {
				todo = append(todo, it)
			}
		}
		if len(todo) == 0 {
			return startResult{startToast, "这些文件已经用同样的设置转换过了；改了设置再点就会重新转换", ""}
		}
	}
	if t.ID == "pdf:merge" && len(todo) < 2 {
		return startResult{startWarn, "至少要两个 PDF 才能合并", ""}
	}
	sess := c.sess
	if sess == nil {
		sess = &session{page: c.cur, started: time.Now()}
	}
	dirs := map[string]string{} // 同一个文件夹只检查一次能不能写
	outDir := func(in string) string {
		d := filepath.Dir(in)
		if v, ok := dirs[d]; ok {
			return v
		}
		v := c.outDirFor(in)
		dirs[d] = v
		return v
	}
	if t.Batch {
		var paths []string
		for _, it := range todo {
			paths = append(paths, it.path)
		}
		task := &conv.Task{Job: conv.NewJob(paths, t.ID, opt, outDir(paths[0]), nil), Lane: t.Lane, Run: t.Run}
		for _, it := range todo {
			it.task, it.key, it.outText = task, key, ""
		}
		sess.tasks = append(sess.tasks, task)
		c.runner.Submit(task)
	} else {
		for _, it := range todo {
			task := &conv.Task{Job: conv.NewJob([]string{it.path}, t.ID, opt.Clone(), outDir(it.path), nil), Lane: t.Lane, Run: t.Run, Tag: it}
			it.task, it.key, it.outText = task, key, ""
			sess.tasks = append(sess.tasks, task)
			c.runner.Submit(task)
		}
	}
	c.sess = sess
	return startResult{kind: startOK}
}

// prepareRedo 准备重新转换这一行：返回交给 start 的 only（合并出来的文件整批重新合并，返回 nil）
func (c *core) prepareRedo(it *item) *item {
	if it.task != nil && len(it.task.Job.Inputs) > 1 {
		for _, other := range c.page().items {
			if other.task == it.task {
				other.key = ""
			}
		}
		return nil
	}
	it.key = "" // 和当前设置对不上，就会重新转
	return it
}

// outDirFor 决定输出文件放哪：默认和原文件放在一起；那里不能写时放到「文稿/万能格式转换」
func (c *core) outDirFor(input string) string {
	if c.outMode == "custom" && c.outDir != "" {
		return c.outDir
	}
	dir := filepath.Dir(input)
	if writable(dir) {
		return dir
	}
	d := defaultOutDir()
	os.MkdirAll(d, 0o755)
	return d
}

// finishSession 在这一批任务全部结束时调用：清掉这一批，返回给用户看的总结和第一个结果（「打开文件夹」用）
func (c *core) finishSession() (msg string, isErr bool, firstOut string) {
	ok, failed, stopped := 0, 0, 0
	var outs []string
	seen := map[*conv.Task]bool{}
	for _, t := range c.sess.tasks {
		if seen[t] {
			continue
		}
		seen[t] = true
		info := c.runner.Snapshot(t)
		switch info.State {
		case conv.Done:
			ok += len(t.Job.Inputs)
			outs = append(outs, info.Outputs...)
		case conv.Failed:
			failed += len(t.Job.Inputs)
		default:
			stopped += len(t.Job.Inputs)
		}
	}
	c.sess = nil
	switch {
	case failed == 0 && stopped == 0:
		msg = "全部完成，转换了 " + itoa(ok) + " 个文件"
	case ok == 0 && failed == 0:
		msg = "已停止"
	default:
		msg = "完成 " + itoa(ok) + " 个"
		if failed > 0 {
			msg += "，失败 " + itoa(failed) + " 个（原因见列表）"
		}
		if stopped > 0 {
			msg += "，停止 " + itoa(stopped) + " 个"
		}
	}
	if len(outs) > 0 {
		firstOut = outs[0]
	}
	return msg, failed > 0 && ok == 0, firstOut
}

// statusText 底栏上的进度说明，比如「正在转换 3 / 10 · 1:05」
func (c *core) statusText() string {
	if c.sess == nil {
		return ""
	}
	done, total := c.sessionCount()
	el := int(time.Since(c.sess.started).Seconds())
	return fmt.Sprintf("正在转换 %d / %d · %d:%02d", done, total, el/60, el%60)
}

// sessionCount 是这一批里已经结束的任务数和总数
func (c *core) sessionCount() (done, total int) {
	seen := map[*conv.Task]bool{}
	for _, t := range c.sess.tasks {
		if seen[t] {
			continue
		}
		seen[t] = true
		total++
		if s := c.runner.Snapshot(t).State; s == conv.Done || s == conv.Failed || s == conv.Cancelled {
			done++
		}
	}
	return done, total
}

// overallProgress 这一批任务的总进度（0~1）
func (c *core) overallProgress() float64 {
	if c.sess == nil {
		return 0
	}
	seen := map[*conv.Task]bool{}
	var sum float64
	n := 0
	for _, t := range c.sess.tasks {
		if seen[t] {
			continue
		}
		seen[t] = true
		n++
		info := c.runner.Snapshot(t)
		switch info.State {
		case conv.Done, conv.Failed, conv.Cancelled:
			sum++
		case conv.Running:
			sum += max(info.Progress.Frac, 0)
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// ---------------------------------------------------------------- 每一行显示什么

// tone 是文字的颜色，各平台自己决定具体用什么色
type tone int

const (
	toneText   tone = iota // 正文
	toneText2              // 次要
	toneText3              // 更淡（不支持、等待）
	toneAccent             // 蓝色（进度）
	toneOK                 // 绿色（完成）
	toneDanger             // 红色（失败）
)

// rowState 是「状态」一栏：文字、颜色、进度（running 时画进度条，frac < 0 表示说不准）
func (c *core) rowState(it *item) (state string, color tone, frac float64, running bool) {
	t := c.page().target
	if it.task == nil {
		if !t.Accept(it.path) {
			return "不支持", toneText3, 0, false
		}
		return "等待开始", toneText3, 0, false
	}
	info := c.runner.Snapshot(it.task)
	switch info.State {
	case conv.Waiting:
		return "排队中", toneText2, 0, false
	case conv.Running:
		return "", toneAccent, info.Progress.Frac, true
	case conv.Done:
		return "✓ 完成", toneOK, 1, false
	case conv.Failed:
		return "✗ 失败", toneDanger, 0, false
	default:
		return "已停止", toneText3, 0, false
	}
}

// rowResult 是「结果」一栏的文字
func (c *core) rowResult(it *item) string {
	t := c.page().target
	if it.task == nil {
		if !t.Accept(it.path) {
			return "这种文件不能转成「" + t.Label + "」"
		}
		return ""
	}
	info := c.runner.Snapshot(it.task)
	switch info.State {
	case conv.Running:
		note := info.Progress.Note
		if note == "" {
			note = "正在转换…"
		}
		return note
	case conv.Done:
		if it.outText == "" {
			it.outText = describeOutputs(info.Outputs, info.Progress.Note)
		}
		return it.outText
	case conv.Failed:
		return errText(info.Err)
	}
	return ""
}

// rowTones 是「文件」一栏和「结果」一栏的颜色
func (c *core) rowTones(it *item) (name, result tone) {
	unsupported := it.task == nil && !c.page().target.Accept(it.path)
	name, result = toneText, toneText2
	if unsupported {
		name, result = toneText3, toneText3
	}
	if it.task != nil && c.runner.Snapshot(it.task).State == conv.Failed {
		result = toneDanger
	}
	return name, result
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	if ue, ok := err.(*conv.UserError); ok {
		return ue.Msg
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// errDetail 是失败原因里的技术细节
func errDetail(err error) string {
	if ue, ok := err.(*conv.UserError); ok {
		return ue.Detail
	} else if err != nil {
		return err.Error()
	}
	return ""
}

// describeOutputs 写成「照片 (1).jpg · 230 KB」或「照片/（12 个文件，3 MB）」
func describeOutputs(outs []string, note string) string {
	if len(outs) == 0 {
		return note
	}
	var parts []string
	for _, o := range outs {
		fi, err := os.Stat(o)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			n := 0
			var size int64
			filepath.WalkDir(o, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					n++
					if i, e := d.Info(); e == nil {
						size += i.Size()
					}
				}
				return nil
			})
			parts = append(parts, fmt.Sprintf("%s%c（%d 个文件，%s）", filepath.Base(o), filepath.Separator, n, humanSize(size)))
		} else {
			parts = append(parts, filepath.Base(o)+" · "+humanSize(fi.Size()))
		}
	}
	s := strings.Join(parts, "；")
	if len(parts) > 3 {
		s = fmt.Sprintf("%s 等 %d 个文件", parts[0], len(parts))
	}
	if i := strings.Index(note, "，第 "); i > 0 {
		note = note[:i] // 去掉「第 2 遍（共 2 遍）」这种过程中的进度
	}
	if note != "" && !strings.HasPrefix(note, "正在") && !strings.HasPrefix(note, "第") && !strings.HasSuffix(note, "…") {
		s += "（" + note + "）" // 比如「从 12 MB 压缩到 3 MB」「只保留前 40 秒」
	}
	return "→ " + s
}

// ---------------------------------------------------------------- 页面上的说明

// officeBanner 文档页上说明 Office 的情况：没有能用的软件时提醒（warn 为 true）
func (c *core) officeBanner() (string, bool) {
	if c.page().page.Kind != conv.Doc {
		return "", false
	}
	if catalog.OfficeInfo() == "" {
		return noOfficeBanner, true
	}
	return "", false
}

// hintFor 是选中的目标下面那一句说明
func hintFor(t *catalog.Target) string {
	if t.ID == "pdf:docx" {
		if catalog.OfficeInfo() == "" {
			return noWordHint
		}
		if pdfToWordHint != "" {
			return pdfToWordHint
		}
	}
	return t.Hint
}

// aboutText 是「关于」里的说明
func aboutText() string {
	content := "版本 " + version + "\n\n" +
		"图片、视频、音频、Word / Excel / PPT、PDF 之间的格式转换，全部在你的电脑上完成，文件不会上传到任何地方。\n\n" +
		"用到的开源组件：FFmpeg（视频和音频）、ImageMagick（图片）、PDFium 与 pdfcpu（PDF）。" + officeAbout
	if info := catalog.OfficeInfo(); info != "" {
		content += "\n\n文档转换使用：" + info
	}
	return content
}

func itoa(n int) string { return formatCount(int64(n)) }

// ---------------------------------------------------------------- 记住上次用的设置

type savedPage struct {
	Target string       `json:"target"`
	Opts   conv.Options `json:"opts"`
}

type savedSettings struct {
	Page    int                  `json:"page"`
	Pages   map[string]savedPage `json:"pages"`
	OutMode string               `json:"outMode"`
	OutDir  string               `json:"outDir"`
}

// 密码不保存
var unsavedKeys = []string{conv.OptPassword}

func (c *core) loadSettings() {
	if shotFile != "" {
		return // 截图模式用默认设置，结果才稳定
	}
	b, err := os.ReadFile(filepath.Join(settingsDir(), "settings.json"))
	if err != nil {
		return
	}
	var s savedSettings
	if json.Unmarshal(b, &s) != nil {
		return
	}
	if s.Page >= 0 && s.Page < len(c.pages) {
		c.cur = s.Page
	}
	for _, ps := range c.pages {
		sp, ok := s.Pages[ps.page.Kind.Name()]
		if !ok {
			continue
		}
		if t := catalog.ByID(sp.Target); t != nil && t.Page == ps.page.Kind {
			ps.target = t
		}
		if sp.Opts != nil {
			ps.opts = sp.Opts
		}
	}
	if s.OutMode == "custom" && s.OutDir != "" {
		c.outMode, c.outDir = "custom", s.OutDir
	} else {
		c.outDir = s.OutDir
	}
}

func (c *core) saveSettings() {
	if shotFile != "" {
		return
	}
	s := savedSettings{Page: c.cur, Pages: map[string]savedPage{}, OutMode: c.outMode, OutDir: c.outDir}
	for _, ps := range c.pages {
		o := ps.opts.Clone()
		for _, k := range unsavedKeys {
			delete(o, k)
		}
		s.Pages[ps.page.Kind.Name()] = savedPage{Target: ps.target.ID, Opts: o}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	dir := settingsDir()
	os.MkdirAll(dir, 0o755)
	tmp := filepath.Join(dir, "settings.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(dir, "settings.json"))
	}
}
