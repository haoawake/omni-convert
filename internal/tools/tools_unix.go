//go:build !windows

package tools

// macOS 上打包好的「万能格式转换.app」里：
//
//	Contents/MacOS/万能格式转换、ffmpeg、ffprobe、magick
//	Contents/Frameworks/libpdfium.dylib 和 ImageMagick 用到的 dylib
//	Contents/Resources/magick/ ImageMagick 的配置文件
//
// 开发时（go run / go test）用 scripts/fetch-tools-mac.sh 下载到 tools/：
//
//	tools/ffmpeg/ffmpeg、ffprobe
//	tools/magick/bin/magick、tools/magick/lib/*.dylib、tools/magick/config/
//	tools/pdfium/libpdfium.dylib

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	bundleOnce sync.Once
	bundle     string
)

// bundleContents 是程序所在的 .app 里的 Contents 文件夹；不是从 .app 里运行时返回空字符串
func bundleContents() string {
	bundleOnce.Do(func() {
		if os.Getenv("OMNI_TOOLS") != "" {
			return
		}
		exe, err := os.Executable()
		if err != nil {
			return
		}
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		dir := filepath.Dir(exe)
		contents := filepath.Dir(dir)
		if filepath.Base(dir) == "MacOS" && filepath.Base(contents) == "Contents" && exists(filepath.Join(dir, "ffmpeg")) {
			bundle = contents
		}
	})
	return bundle
}

func FFmpeg() string {
	if c := bundleContents(); c != "" {
		return filepath.Join(c, "MacOS", "ffmpeg")
	}
	return path("ffmpeg", "ffmpeg")
}

func FFprobe() string {
	if c := bundleContents(); c != "" {
		return filepath.Join(c, "MacOS", "ffprobe")
	}
	return path("ffmpeg", "ffprobe")
}

func Magick() string {
	if c := bundleContents(); c != "" {
		return filepath.Join(c, "MacOS", "magick")
	}
	return path("magick", "bin", "magick")
}

func PdfiumDLL() string {
	if c := bundleContents(); c != "" {
		return filepath.Join(c, "Frameworks", "libpdfium.dylib")
	}
	return path("pdfium", "libpdfium.dylib")
}

// magickConfig 是 ImageMagick 配置文件所在的文件夹
func magickConfig() string {
	if c := bundleContents(); c != "" {
		return filepath.Join(c, "Resources", "magick")
	}
	return path("magick", "config")
}

// systemSRGB 是 macOS 自带的 sRGB 色彩配置文件
const systemSRGB = "/System/Library/ColorSync/Profiles/sRGB Profile.icc"

// SRGBProfile 是 sRGB 色彩配置文件（CMYK、Display P3 的照片转成 sRGB 时用），没有时返回空字符串
func SRGBProfile() string {
	if p := filepath.Join(magickConfig(), "sRGB.icc"); exists(p) {
		return p
	}
	if exists(systemSRGB) {
		return systemSRGB
	}
	return ""
}

const missingHint = "程序不完整，请重新下载「万能格式转换」"

// magickEnv 让 ImageMagick 只用自带的配置；字体配置指向系统字体（SVG 里的文字要用）
func magickEnv(exe string) []string {
	cfg := magickConfig()
	env := []string{
		"MAGICK_HOME=" + filepath.Dir(cfg),
		"MAGICK_CONFIGURE_PATH=" + cfg,
	}
	if fc := filepath.Join(cfg, "fonts.conf"); exists(fc) {
		env = append(env, "FONTCONFIG_FILE="+fc)
	}
	return env
}
