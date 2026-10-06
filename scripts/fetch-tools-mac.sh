#!/usr/bin/env bash
# 下载 macOS（Apple 芯片）用的转换引擎到 tools/（只能在 macOS 上运行）：
#   FFmpeg、FFprobe  Martin Riedl 的静态编译版（https://ffmpeg.martin-riedl.de），
#                    带 x264、x265、VP9、AV1、WebP、LAME、Opus、Vorbis、zimg 和苹果的 VideoToolbox 硬件编码
#   ImageMagick      conda-forge 的编译版（读写 HEIC、AVIF、WebP、JPEG XL、TIFF、RAW、SVG……），
#                    按 scripts/mac/imagemagick-osx-arm64.lock 里固定的包装进一个临时环境，
#                    再只拷出 magick 真正用到的 dylib（全部是 @rpath 引用，可以整体搬走）
#   PDFium           bblanchon/pdfium-binaries 的 mac-arm64 版
#
#   用法：scripts/fetch-tools-mac.sh          按 lock 文件下载
#        scripts/fetch-tools-mac.sh --lock   重新解析 ImageMagick 的依赖，更新 lock 文件
#
# 版本和下载地址都固定住（并校验 SHA-256），保证每次发版用的是同一套引擎。
# 结果的目录结构见 internal/tools/tools_unix.go；scripts/build-mac.sh 再把它们装进 .app。
set -euo pipefail
cd "$(dirname "$0")/.."

[ "$(uname -s)" = Darwin ] || { echo "只能在 macOS 上运行" >&2; exit 1; }

FFMPEG_VER=9.0.2
FFMPEG_BASE="https://ffmpeg.martin-riedl.de/download/macos/arm64/1789931890_9.0.2"
FFMPEG_SHA=c8ed4c4e6978a03c485edbfe4e0a5dc2380f8a30bba5150531b31b094492d924
FFPROBE_SHA=fcbe839537485eaee7a7a8bc5cbc0f90d53617e80943e8a5b2e31cb851197ea6
MICROMAMBA_URL="https://github.com/mamba-org/micromamba-releases/releases/download/2.9.0-0/micromamba-osx-arm64"
MICROMAMBA_SHA=ec2a072f028e1a7cf20f3e2e74d5a8127cf5a5f27636375b5359811565f4e5be
IM_SPEC="imagemagick=7.1.2_31=imagemagick_hcf8ddec_1" # 不带 Ghostscript（AGPL）的那个变体
LOCK=scripts/mac/imagemagick-osx-arm64.lock
PDFIUM_URL="https://github.com/bblanchon/pdfium-binaries/releases/download/chromium/8076/pdfium-mac-arm64.tgz"

relock=0
[ "${1:-}" = --lock ] && relock=1

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
rm -rf tools
mkdir -p tools/ffmpeg tools/magick/bin tools/magick/lib tools/magick/config tools/magick/licenses tools/pdfium

fetch() { # fetch URL 文件 [sha256]
	curl -fsSL --retry 3 -o "$2" "$1"
	if [ -n "${3:-}" ]; then
		echo "$3  $2" | shasum -a 256 -c - > /dev/null || { echo "校验失败：$1" >&2; exit 1; }
	fi
}

echo "下载 FFmpeg ${FFMPEG_VER}…"
fetch "$FFMPEG_BASE/ffmpeg.zip" "$tmp/ffmpeg.zip" "$FFMPEG_SHA"
fetch "$FFMPEG_BASE/ffprobe.zip" "$tmp/ffprobe.zip" "$FFPROBE_SHA"
unzip -q -o "$tmp/ffmpeg.zip" -d tools/ffmpeg
unzip -q -o "$tmp/ffprobe.zip" -d tools/ffmpeg
chmod +x tools/ffmpeg/ffmpeg tools/ffmpeg/ffprobe
{
	echo "FFmpeg ${FFMPEG_VER}（macOS arm64 静态编译版）"
	echo "来自 https://ffmpeg.martin-riedl.de ，编译脚本 https://git.martin-riedl.de/ffmpeg/build-script"
	echo "FFmpeg 按 GPL v3 发布，源码见 https://ffmpeg.org 。编译配置："
	echo
	tools/ffmpeg/ffmpeg -hide_banner -buildconf 2>&1 || true
	echo
	echo "======================================================================"
	echo
	curl -fsSL "https://raw.githubusercontent.com/FFmpeg/FFmpeg/n$FFMPEG_VER/LICENSE.md" || true
	echo
	echo "======================================================================"
	echo
	curl -fsSL "https://raw.githubusercontent.com/FFmpeg/FFmpeg/n$FFMPEG_VER/COPYING.GPLv3"
} > tools/ffmpeg/LICENSE.txt

echo "下载 ImageMagick（conda-forge）…"
fetch "$MICROMAMBA_URL" "$tmp/micromamba" "$MICROMAMBA_SHA"
chmod +x "$tmp/micromamba"
export MAMBA_ROOT_PREFIX="$tmp/mamba"
env="$tmp/env"
if [ $relock = 1 ] || [ ! -f "$LOCK" ]; then
	"$tmp/micromamba" create -q -y -p "$env" -c conda-forge --override-channels "$IM_SPEC"
	mkdir -p "$(dirname "$LOCK")"
	{
		echo "# ImageMagick 和它的依赖（conda-forge，osx-arm64），由 scripts/fetch-tools-mac.sh --lock 生成"
		"$tmp/micromamba" env export -p "$env" --explicit --md5 | grep -v '^#'
	} > "$LOCK"
	echo "已更新 $LOCK"
else
	"$tmp/micromamba" create -q -y -p "$env" --file "$LOCK"
fi

# 从 magick 开始，顺着 @rpath 引用找出所有要用的 dylib
cp "$env/bin/magick" tools/magick/bin/
queue=(tools/magick/bin/magick)
while [ ${#queue[@]} -gt 0 ]; do
	f=${queue[0]}
	queue=("${queue[@]:1}")
	for dep in $(otool -L "$f" | tail -n +2 | awk '{print $1}'); do
		case "$dep" in
		/usr/lib/* | /System/*) ;;
		@rpath/*)
			name=${dep#@rpath/}
			if [ ! -e "tools/magick/lib/$name" ]; then
				[ -e "$env/lib/$name" ] || { echo "找不到 ${name}（$f 要用）" >&2; exit 1; }
				cp -L "$env/lib/$name" "tools/magick/lib/$name"
				chmod u+w "tools/magick/lib/$name"
				queue+=("tools/magick/lib/$name")
			fi
			;;
		*)
			echo "$f 引用了不能搬走的库：$dep" >&2
			exit 1
			;;
		esac
	done
done
# 拷出来的文件重新签名（ad-hoc），Apple 芯片上没有签名的代码不能运行
for f in tools/magick/bin/magick tools/magick/lib/*.dylib; do
	codesign --force --sign - "$f" 2> /dev/null
done

# 配置文件放在一起，运行时用 MAGICK_CONFIGURE_PATH 指过去（见 internal/tools）
cp "$env"/etc/ImageMagick-7/*.xml tools/magick/config/
cp "$env"/lib/ImageMagick-*/config-Q16HDRI/configure.xml tools/magick/config/
cp "$env"/share/ImageMagick-7/english.xml "$env"/share/ImageMagick-7/locale.xml tools/magick/config/
# 字体配置：用系统自带的字体（SVG 里有文字时用到），缓存放在用户自己的「资源库/Caches」里
cat > tools/magick/config/fonts.conf << 'XML'
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
	<dir>/System/Library/Fonts</dir>
	<dir>/Library/Fonts</dir>
	<dir>~/Library/Fonts</dir>
	<cachedir>~/Library/Caches/万能格式转换/fontconfig</cachedir>
	<config><rescan><int>0</int></rescan></config>
</fontconfig>
XML

# 许可证：ImageMagick 和拷出来的每个依赖各一份
for meta in "$env"/conda-meta/*.json; do
	pkg=$(basename "$meta" .json)
	lic="$MAMBA_ROOT_PREFIX/pkgs/$pkg/info/licenses"
	[ -d "$lic" ] || continue
	name=${pkg%-*-*}
	mkdir -p "tools/magick/licenses/$name"
	cp -R "$lic"/. "tools/magick/licenses/$name/"
done
cp "tools/magick/licenses/imagemagick/LICENSE" tools/magick/LICENSE.txt 2> /dev/null || true
cp "$LOCK" tools/magick/

echo "下载 PDFium…"
fetch "$PDFIUM_URL" "$tmp/pdfium.tgz"
mkdir -p "$tmp/pdfium"
tar -xzf "$tmp/pdfium.tgz" -C "$tmp/pdfium"
cp "$tmp"/pdfium/lib/libpdfium.dylib "$tmp"/pdfium/LICENSE "$tmp"/pdfium/VERSION tools/pdfium/
codesign --force --sign - tools/pdfium/libpdfium.dylib 2> /dev/null

du -sh tools/*
