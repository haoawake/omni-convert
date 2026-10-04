//go:build windows

package office

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

// 通过注册表找 Office / WPS / LibreOffice / Edge，不启动任何程序。

// 每种程序可能的 ProgID，按优先级排列
var progCandidates = [numApps][]string{
	appWord:  {"Word.Application", "KWPS.Application", "WPS.Application"},
	appExcel: {"Excel.Application", "KET.Application", "ET.Application"},
	appPPT:   {"PowerPoint.Application", "KWPP.Application", "WPP.Application"},
}

func detectSystem() (Info, [numApps]progInfo) {
	var progs [numApps]progInfo
	for k := range progs {
		progs[k] = findProg(appKind(k))
	}
	info := Info{
		Word:        progs[appWord].vendor,
		Excel:       progs[appExcel].vendor,
		PowerPoint:  progs[appPPT].vendor,
		LibreOffice: findSoffice(),
		Edge:        findEdge(),
	}
	return info, progs
}

// findProg 找到第一个确实装好了的 ProgID（注册表里登记的程序文件要存在）
func findProg(k appKind) progInfo {
	for _, id := range progCandidates[k] {
		clsid := regString(registry.CLASSES_ROOT, id+`\CLSID`, "")
		if clsid == "" {
			continue
		}
		exe := ""
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			if s := regStringView(registry.CLASSES_ROOT, `CLSID\`+clsid+`\LocalServer32`, "", view); s != "" {
				exe = exePath(s)
				if exe != "" {
					break
				}
			}
		}
		if exe == "" {
			continue
		}
		base := strings.ToUpper(filepath.Base(exe))
		vendor := vendorWPS
		switch base {
		case "WINWORD.EXE", "EXCEL.EXE", "POWERPNT.EXE":
			vendor = vendorMS
		}
		return progInfo{vendor: vendor, progID: id, exe: filepath.Base(exe), path: exe}
	}
	return progInfo{}
}

// exePath 从 LocalServer32 的值（可能带引号和参数）里取出程序路径，文件不存在时返回空
func exePath(s string) string {
	if e, err := registry.ExpandString(s); err == nil { // 值里可能有 %ProgramFiles% 这样的变量
		s = e
	}
	s = strings.TrimSpace(s)
	var p string
	if strings.HasPrefix(s, `"`) {
		if i := strings.Index(s[1:], `"`); i >= 0 {
			p = s[1 : i+1]
		}
	} else if i := strings.Index(strings.ToLower(s), ".exe"); i >= 0 {
		p = s[:i+4]
	}
	if p == "" {
		return ""
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
}

func regString(root registry.Key, path, name string) string {
	return regStringView(root, path, name, 0)
}

func regStringView(root registry.Key, path, name string, view uint32) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|view)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return s
}

// findSoffice 找 LibreOffice 的 soffice.exe
func findSoffice() string {
	var cands []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			if s := regStringView(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\soffice.exe`, "", view); s != "" {
				cands = append(cands, strings.Trim(s, `"`))
			}
			if s := regStringView(root, `SOFTWARE\LibreOffice\UNO\InstallPath`, "", view); s != "" {
				cands = append(cands, filepath.Join(s, "soffice.exe"))
			}
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if d := os.Getenv(env); d != "" {
			cands = append(cands, filepath.Join(d, "LibreOffice", "program", "soffice.exe"))
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// findEdge 找 Microsoft Edge
func findEdge() string {
	var cands []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		if s := regString(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`, ""); s != "" {
			cands = append(cands, strings.Trim(s, `"`))
		}
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if d := os.Getenv(env); d != "" {
			cands = append(cands, filepath.Join(d, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, c := range cands {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// ---------------------------------------------------------------- Word 打开 PDF 的提示

// allowPDFOpen 让 Word 打开 PDF 时不弹「Word 现在会将 PDF 转换为可编辑的 Word 文档」。
// 这个提示框没有窗口句柄，DisplayAlerts=0 也挡不住，不关掉的话 Word 会一直卡着。
// 返回的函数把设置还原成原来的样子。
func allowPDFOpen(version string) (restore func()) {
	if version == "" {
		version = "16.0"
	}
	path := `Software\Microsoft\Office\` + version + `\Word\Options`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return func() {}
	}
	old, _, errOld := k.GetIntegerValue("DisableConvertPdfWarning")
	if errOld == nil && old == 1 {
		k.Close()
		return func() {}
	}
	k.SetDWordValue("DisableConvertPdfWarning", 1)
	return func() {
		defer k.Close()
		if errOld == nil {
			k.SetDWordValue("DisableConvertPdfWarning", uint32(old))
		} else {
			k.DeleteValue("DisableConvertPdfWarning")
		}
	}
}

// ---------------------------------------------------------------- 崩溃记录

var reOfficeVer = regexp.MustCompile(`^\d+\.\d+$`)

// cleanResiliency 删掉 Office 记下的「上次打开某文件时出了严重错误」记录（只删我们临时文件夹里的）。
// 强制结束过 Office 之后不删的话，下次打开同名文件会弹「是否仍要打开它」。
func cleanResiliency() {
	root, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Office`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return
	}
	vers, _ := root.ReadSubKeyNames(-1)
	root.Close()
	marker := utf16Bytes(tempMarker)
	for _, v := range vers {
		if !reOfficeVer.MatchString(v) {
			continue
		}
		for _, app := range []string{"Word", "Excel", "PowerPoint"} {
			base := `Software\Microsoft\Office\` + v + `\` + app + `\Resiliency`
			cleanValues(base+`\DisabledItems`, marker)
			cleanValues(base+`\StartupItems`, marker)
			if k, err := registry.OpenKey(registry.CURRENT_USER, base+`\DocumentRecovery`, registry.ENUMERATE_SUB_KEYS); err == nil {
				subs, _ := k.ReadSubKeyNames(-1)
				k.Close()
				for _, s := range subs {
					p := base + `\DocumentRecovery\` + s
					if keyMentions(p, marker) {
						registry.DeleteKey(registry.CURRENT_USER, p)
					}
				}
			}
		}
	}
}

// resilState 记下某个程序的 Resiliency 注册表里有哪些键和值。
// 强制结束 Office 后，它启动时写下的「正在运行」记录会留下来，下次启动可能问「是否以安全模式启动」，
// 所以启动前记一份，结束后把多出来的删掉。
type resilState map[string]bool

var regAppName = [numApps]string{"Word", "Excel", "PowerPoint"}

func resilRoots(k appKind) []string {
	root, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Office`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	vers, _ := root.ReadSubKeyNames(-1)
	root.Close()
	var out []string
	for _, v := range vers {
		if reOfficeVer.MatchString(v) {
			out = append(out, `Software\Microsoft\Office\`+v+`\`+regAppName[k]+`\Resiliency`)
		}
	}
	return out
}

func snapshotResiliency(k appKind) resilState {
	st := resilState{}
	var walk func(path string)
	walk = func(path string) {
		key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
		if err != nil {
			return
		}
		st[path] = true
		names, _ := key.ReadValueNames(-1)
		subs, _ := key.ReadSubKeyNames(-1)
		key.Close()
		for _, n := range names {
			st[path+"|"+n] = true
		}
		for _, s := range subs {
			walk(path + `\` + s)
		}
	}
	for _, r := range resilRoots(k) {
		walk(r)
	}
	return st
}

// restoreResiliency 删掉 before 之后新出现的 Resiliency 记录
func restoreResiliency(k appKind, before resilState) {
	if before == nil {
		return
	}
	now := snapshotResiliency(k)
	marker := utf16Bytes(tempMarker)
	var keys []string
	for item := range now {
		if before[item] {
			continue
		}
		// Word、Excel 每次都是新进程，文档恢复记录可能是用户自己同时崩溃的 Word 留下的（能找回没保存的文档），
		// 只删明确指向我们临时文件夹的那些。PowerPoint 只有一个进程，被我们结束的那个一定是我们自己的。
		if i := strings.Index(item, `\DocumentRecovery\`); i >= 0 && k != appPPT {
			rest := item[i+len(`\DocumentRecovery\`):]
			sub, _, _ := strings.Cut(rest, `\`)
			sub, _, _ = strings.Cut(sub, "|")
			if !keyMentions(item[:i]+`\DocumentRecovery\`+sub, marker) {
				continue
			}
		}
		if path, name, ok := strings.Cut(item, "|"); ok {
			if key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE); err == nil {
				key.DeleteValue(name)
				key.Close()
			}
		} else {
			keys = append(keys, item)
		}
	}
	// 新出现的子键从最深的开始删
	for len(keys) > 0 {
		deepest := 0
		for i, p := range keys {
			if strings.Count(p, `\`) > strings.Count(keys[deepest], `\`) {
				deepest = i
			}
		}
		registry.DeleteKey(registry.CURRENT_USER, keys[deepest])
		keys = append(keys[:deepest], keys[deepest+1:]...)
	}
}

func utf16Bytes(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func cleanValues(path string, marker []byte) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	for _, n := range names {
		if b, _, err := k.GetBinaryValue(n); err == nil && bytes.Contains(bytes.ToLower(b), marker) {
			k.DeleteValue(n)
		}
	}
}

func keyMentions(path string, marker []byte) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	for _, n := range names {
		if b, _, err := k.GetBinaryValue(n); err == nil && bytes.Contains(bytes.ToLower(b), marker) {
			return true
		}
	}
	return false
}
