#!/usr/bin/env bash
# 下载转换引擎到 tools/：FFmpeg（视频、音频）、ImageMagick（图片）、PDFium（PDF 渲染）。
#   用法：scripts/fetch-tools.sh
# 版本都固定住，保证每次发版用的是同一套引擎。需要 curl、tar、7z（Ubuntu 上是 p7zip-full）。
set -euo pipefail
cd "$(dirname "$0")/.."

FFMPEG_URL="https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-10-03-18-14/ffmpeg-n9.0.2-22-g46d8f462ee-win64-gpl-shared-9.0.zip"
MAGICK_URL="https://github.com/ImageMagick/ImageMagick/releases/download/7.1.2-32/ImageMagick-7.1.2-32-portable-Q16-HDRI-x64.7z"
PDFIUM_URL="https://github.com/bblanchon/pdfium-binaries/releases/download/chromium/8076/pdfium-win-x64.tgz"

SEVENZIP=$(command -v 7z || command -v 7za || true)
[ -z "$SEVENZIP" ] && [ -x "/c/Program Files/7-Zip/7z.exe" ] && SEVENZIP="/c/Program Files/7-Zip/7z.exe"
[ -z "$SEVENZIP" ] && { echo "需要 7z 来解压 ImageMagick" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
rm -rf tools
mkdir -p tools/ffmpeg tools/magick tools/pdfium

echo "下载 FFmpeg…"
curl -fsSL -o "$tmp/ffmpeg.zip" "$FFMPEG_URL"
unzip -q "$tmp/ffmpeg.zip" -d "$tmp/ff"
ff=$(echo "$tmp"/ff/*/)
cp "$ff"bin/ffmpeg.exe "$ff"bin/ffprobe.exe "$ff"bin/*.dll tools/ffmpeg/
cp "$ff"LICENSE.txt tools/ffmpeg/LICENSE.txt

echo "下载 ImageMagick…"
curl -fsSL -o "$tmp/magick.7z" "$MAGICK_URL"
"$SEVENZIP" x -y -o"$tmp/magick" "$tmp/magick.7z" > /dev/null
cp "$tmp"/magick/magick.exe "$tmp"/magick/*.xml "$tmp"/magick/sRGB.icc "$tmp"/magick/LICENSE.txt "$tmp"/magick/NOTICE.txt tools/magick/

echo "下载 PDFium…"
curl -fsSL -o "$tmp/pdfium.tgz" "$PDFIUM_URL"
mkdir -p "$tmp/pdfium"
tar -xzf "$tmp/pdfium.tgz" -C "$tmp/pdfium"
cp "$tmp"/pdfium/bin/pdfium.dll "$tmp"/pdfium/LICENSE "$tmp"/pdfium/VERSION tools/pdfium/

du -sh tools/*
