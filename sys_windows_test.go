//go:build windows

package main

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// 右键菜单：加上、确认注册表里有、再去掉。只动当前用户的 HKCU，测完恢复原样。
func TestContextMenuInstallUninstall(t *testing.T) {
	if testing.Short() {
		t.Skip("会改注册表，-short 时跳过")
	}
	was := contextMenuInstalled()
	if err := installContextMenu(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if was {
			installContextMenu()
		}
	}()
	if !contextMenuInstalled() {
		t.Fatal("加上之后应该能检测到")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\SystemFileAssociations\.heic\shell\`+menuVerb+`\command`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal("HEIC 的右键菜单没加上：", err)
	}
	v, _, _ := k.GetStringValue("")
	k.Close()
	if !strings.HasSuffix(v, `" "%1"`) {
		t.Fatalf("命令不对：%s", v)
	}
	if err := uninstallContextMenu(); err != nil {
		t.Fatal(err)
	}
	if contextMenuInstalled() {
		t.Fatal("去掉之后不应该还在")
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\Directory\shell\`+menuVerb, registry.QUERY_VALUE); err == nil {
		t.Fatal("文件夹的右键菜单没删干净")
	}
}

func TestShortPath(t *testing.T) {
	if got := shortPath(`D:\a\b\c\d\e.txt`, 10); got != `D:\…\d\e.txt` {
		t.Errorf("得到 %s", got)
	}
}
