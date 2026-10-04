//go:build windows

package pdf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// 用真的 PowerPoint、Word 打开生成的文件，确认不会弹出「需要修复」。没装 Office 时跳过。
const officeScript = `param([string]$kind, [string]$path)
$ErrorActionPreference = 'Stop'
$miss = [System.Reflection.Missing]::Value
if ($kind -eq 'ppt') {
  $before = @(Get-Process POWERPNT -ErrorAction SilentlyContinue).Count
  $app = New-Object -ComObject PowerPoint.Application
  try {
    $app.DisplayAlerts = 2
    $p = $app.Presentations.Open($path, $true, $false, $false)
    "SLIDES=" + $p.Slides.Count
    "WIDTH=" + [int]$p.PageSetup.SlideWidth
    "HEIGHT=" + [int]$p.PageSetup.SlideHeight
    $t = ''
    foreach ($s in $p.Slides.Item(2).NotesPage.Shapes) { if ($s.HasTextFrame) { $t += $s.TextFrame.TextRange.Text } }
    "NOTES=" + $t.Length
    $p.Close()
  } finally {
    if ($before -eq 0 -and $app.Presentations.Count -eq 0) { $app.Quit() }
  }
} else {
  $old = @(Get-Process WINWORD -ErrorAction SilentlyContinue | ForEach-Object { $_.Id })
  $app = New-Object -ComObject Word.Application
  $mine = @(Get-Process WINWORD -ErrorAction SilentlyContinue | Where-Object { $old -notcontains $_.Id }).Count -gt 0
  try {
    $app.DisplayAlerts = 0
    $d = $app.Documents.Open($path, $false, $true, $false, $miss, $miss, $miss, $miss, $miss, $miss, $miss, $false)
    "PARAS=" + $d.Paragraphs.Count
    "PAGES=" + $d.ComputeStatistics(2)
    $d.Close(0)
  } finally {
    if ($mine -and $app.Documents.Count -eq 0) { $app.Quit() }
  }
}
`

func officeInstalled(progID string) bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, progID+`\CLSID`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func runOffice(t *testing.T, kind, path string) map[string]int {
	t.Helper()
	script := filepath.Join(t.TempDir(), "check.ps1")
	os.WriteFile(script, []byte(officeScript), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, kind, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Office 打不开（可能要修复）：%v\n%s", err, out)
	}
	res := map[string]int{}
	for _, l := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok {
			n, _ := strconv.Atoi(v)
			res[k] = n
		}
	}
	return res
}

func TestOfficeOpensPPT(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式不启动 Office")
	}
	needPdfium(t)
	if !officeInstalled("PowerPoint.Application") {
		t.Skip("没装 PowerPoint")
	}
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, ToPPT(), tj)
	res := runOffice(t, "ppt", tj.Outputs()[0])
	t.Logf("PowerPoint：%v", res)
	if res["SLIDES"] != 3 || res["NOTES"] < 10 || abs(res["WIDTH"]-595) > 2 || abs(res["HEIGHT"]-842) > 2 {
		t.Fatalf("PowerPoint 读出来的不对：%v", res)
	}
}

func TestOfficeOpensDocx(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式不启动 Office")
	}
	needPdfium(t)
	if !officeInstalled("Word.Application") {
		t.Skip("没装 Word")
	}
	tj := newJob(t, []string{textPDF}, nil)
	mustRun(t, ToDocxText(), tj)
	res := runOffice(t, "word", tj.Outputs()[0])
	t.Logf("Word：%v", res)
	if res["PARAS"] < 8 || res["PAGES"] != 3 {
		t.Fatalf("Word 读出来的不对：%v", res)
	}
}

// 文件被别的程序独占时，要给出看得懂的提示
func TestLockedFile(t *testing.T) {
	needPdfium(t)
	p := filepath.Join(t.TempDir(), "被占用 的文件.pdf")
	b, _ := os.ReadFile(textPDF)
	os.WriteFile(p, b, 0o644)
	name, _ := windows.UTF16PtrFromString(p)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(p, "")
	windows.CloseHandle(h)
	if userMsg(err) != "打不开这个文件，可能正被别的程序占用" {
		t.Fatalf("被占用：%v", err)
	}
	if n := pageCount(t, p, ""); n != 3 {
		t.Fatalf("释放后应该能打开：%d", n)
	}
}
