// macplist 生成 macOS 版「万能格式转换.app」的 Info.plist（scripts/build-mac.sh 调用）。
// 能打开的文件类型直接取自 internal/conv 里登记的扩展名，和程序认识的文件保持一致：
// 在访达里对这些文件点右键 →「打开方式」就能看到「万能格式转换」，也能把文件拖到程序坞的图标上。
//
//	go run ./scripts/macplist -version 1.1.0 -min 12.0 > Info.plist
package main

import (
	"flag"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"

	"github.com/haoawake/omni-convert/internal/conv"
)

const appName = "万能格式转换"

// 系统认识的类型（统一类型标识符）。具体的格式（HEIC、RAW、MOV……）都归在 public.image、public.movie 这些大类下面
var contentTypes = []string{
	"public.image",
	"public.movie",
	"public.audio",
	"public.audiovisual-content",
	"com.adobe.pdf",
	"public.plain-text",
	"public.html",
	"public.rtf",
	"public.comma-separated-values-text",
	"net.daringfireball.markdown",
	"org.openxmlformats.wordprocessingml.document",
	"com.microsoft.word.doc",
	"org.openxmlformats.spreadsheetml.sheet",
	"com.microsoft.excel.xls",
	"org.openxmlformats.presentationml.presentation",
	"com.microsoft.powerpoint.ppt",
	"org.oasis-open.opendocument.text",
	"org.oasis-open.opendocument.spreadsheet",
	"org.oasis-open.opendocument.presentation",
	"public.folder",
}

// 访问「桌面」「文稿」「下载」等文件夹时系统弹出的说明
const folderUsage = "万能格式转换要读取你选的文件，并把转换结果保存在原文件旁边（或者你选的文件夹里）。"

func main() {
	ver := flag.String("version", "0.0.0", "版本号")
	minOS := flag.String("min", "12.0", "最低系统版本")
	flag.Parse()

	var exts []string
	for k := conv.Kind(0); k < conv.NumKinds; k++ {
		exts = append(exts, conv.ExtsOf(k)...)
	}
	sort.Strings(exts)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	str := func(s string) string { return "<string>" + html.EscapeString(s) + "</string>" }
	kv := func(k, v string) { w("\t<key>%s</key>\n\t%s\n", k, str(v)) }
	w("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	w("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	w("<plist version=\"1.0\">\n<dict>\n")
	kv("CFBundleDevelopmentRegion", "zh_CN")
	kv("CFBundleDisplayName", appName)
	kv("CFBundleName", appName)
	kv("CFBundleExecutable", appName)
	kv("CFBundleIconFile", "AppIcon")
	kv("CFBundleIdentifier", "com.haoawake.omni-convert")
	kv("CFBundleInfoDictionaryVersion", "6.0")
	kv("CFBundlePackageType", "APPL")
	kv("CFBundleShortVersionString", *ver)
	kv("CFBundleVersion", *ver)
	kv("LSMinimumSystemVersion", *minOS)
	kv("LSApplicationCategoryType", "public.app-category.utilities")
	kv("NSHumanReadableCopyright", "© 2026 haoawake · MIT License")
	kv("NSPrincipalClass", "NSApplication")
	w("\t<key>NSHighResolutionCapable</key>\n\t<true/>\n")
	w("\t<key>NSSupportsAutomaticGraphicsSwitching</key>\n\t<true/>\n")
	w("\t<key>CFBundleLocalizations</key>\n\t<array>\n\t\t%s\n\t</array>\n", str("zh-Hans"))
	for _, k := range []string{"NSDesktopFolderUsageDescription", "NSDocumentsFolderUsageDescription", "NSDownloadsFolderUsageDescription",
		"NSRemovableVolumesUsageDescription", "NSNetworkVolumesUsageDescription"} {
		kv(k, folderUsage)
	}

	// 两组文件类型：一组按系统的类型标识符，一组按扩展名（MKV、WEBM 这类系统不认识的格式靠它）
	w("\t<key>CFBundleDocumentTypes</key>\n\t<array>\n")
	w("\t\t<dict>\n\t\t\t<key>CFBundleTypeName</key>\n\t\t\t%s\n", str("可以转换的文件"))
	w("\t\t\t<key>CFBundleTypeRole</key>\n\t\t\t%s\n", str("Viewer"))
	w("\t\t\t<key>LSHandlerRank</key>\n\t\t\t%s\n", str("Alternate"))
	w("\t\t\t<key>LSItemContentTypes</key>\n\t\t\t<array>\n")
	for _, t := range contentTypes {
		w("\t\t\t\t%s\n", str(t))
	}
	w("\t\t\t</array>\n\t\t</dict>\n")
	w("\t\t<dict>\n\t\t\t<key>CFBundleTypeName</key>\n\t\t\t%s\n", str("其他可以转换的文件"))
	w("\t\t\t<key>CFBundleTypeRole</key>\n\t\t\t%s\n", str("Viewer"))
	w("\t\t\t<key>LSHandlerRank</key>\n\t\t\t%s\n", str("Alternate"))
	w("\t\t\t<key>CFBundleTypeExtensions</key>\n\t\t\t<array>\n")
	for _, e := range exts {
		w("\t\t\t\t%s\n", str(e))
	}
	w("\t\t\t</array>\n\t\t</dict>\n")
	w("\t</array>\n")
	w("</dict>\n</plist>\n")
	os.Stdout.WriteString(b.String())
}
