package tools

import "path/filepath"

func FFmpeg() string    { return path("ffmpeg", "ffmpeg.exe") }
func FFprobe() string   { return path("ffmpeg", "ffprobe.exe") }
func Magick() string    { return path("magick", "magick.exe") }
func PdfiumDLL() string { return path("pdfium", "pdfium.dll") }

// SRGBProfile 是 sRGB 色彩配置文件（CMYK、Display P3 的照片转成 sRGB 时用），没有时返回空字符串
func SRGBProfile() string {
	if p := path("magick", "sRGB.icc"); exists(p) {
		return p
	}
	return ""
}

const missingHint = "请把下载的压缩包完整解压，tools 文件夹要和程序放在一起"

// magickEnv 让 ImageMagick 只用 magick.exe 旁边自带的配置
func magickEnv(exe string) []string {
	dir := filepath.Dir(exe)
	return []string{
		"MAGICK_HOME=" + dir,
		"MAGICK_CONFIGURE_PATH=" + dir,
		"MAGICK_CODER_MODULE_PATH=" + dir,
	}
}
