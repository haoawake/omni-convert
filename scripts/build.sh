#!/usr/bin/env bash
# 构建 Windows 版发布包：dist/OmniConvert-win-x64.zip
#   用法：scripts/build.sh 1.0.0
# 发版时由 .github/workflows/release.yml 在 Ubuntu 上调用；本地用 Git Bash 也能跑。
# 需要先运行 scripts/fetch-tools.sh 下载转换引擎（tools/ 已经存在时跳过）。
#
# 资产名里带上系统和架构（win-x64），工具库靠它自动给访客挑对的安装包。
set -euo pipefail
cd "$(dirname "$0")/.."

VER="${1:-dev}"
APP="万能格式转换"
[ -f tools/ffmpeg/ffmpeg.exe ] && [ -f tools/magick/magick.exe ] && [ -f tools/pdfium/pdfium.dll ] || bash scripts/fetch-tools.sh

rm -rf dist
mkdir -p "dist/$APP"

# 程序图标、「属性 → 详细信息」里的版本号，以及程序清单（高分屏不发虚、用新版系统控件）
go install github.com/tc-hib/go-winres@v0.3.3
WINRES_VER="$VER"
[[ "$VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || WINRES_VER=0.0.0
"$(go env GOPATH)/bin/go-winres" simply \
  --icon winres/icon.png --manifest gui --arch amd64 \
  --product-name "$APP" --file-description "$APP · 图片、视频、音频、文档、PDF 格式转换" \
  --product-version "$WINRES_VER" --file-version "$WINRES_VER" \
  --copyright "© 2026 haoawake · MIT License" --original-filename "$APP.exe"
trap 'rm -f rsrc_windows_*.syso' EXIT

# -H windowsgui：双击打开时不带黑色的命令行窗口
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$VER" -o "dist/$APP/$APP.exe" .

cp -r tools "dist/$APP/tools"
cp LICENSE "dist/$APP/LICENSE.txt"
cat > "dist/$APP/使用说明.txt" <<TXT
$APP $VER

双击「$APP.exe」打开。tools 文件夹里是转换用的组件，要和程序放在一起，别删。

· 把文件拖进窗口，选「转成」什么格式，点「开始转换」。
· 转换结果默认和原文件放在同一个文件夹。
· 点右上角「添加到右键菜单」后，可以在文件上点右键 →「用$APP打开」。
· Word、Excel、PPT 相关的转换需要电脑上装有 Microsoft Office、WPS 或 LibreOffice。

项目主页：https://github.com/haoawake/omni-convert
用到的开源组件：FFmpeg（GPL，源码见 https://ffmpeg.org）、ImageMagick、PDFium、pdfcpu，许可证在 tools 各文件夹里。
TXT

go run ./scripts/pack "dist/$APP" dist/OmniConvert-win-x64.zip
rm -rf "dist/$APP"
(cd dist && sha256sum -- * > SHA256SUMS.txt)
ls -l dist
