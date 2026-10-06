#!/usr/bin/env bash
# 构建 macOS（Apple 芯片）版发布包：dist/OmniConvert-mac-arm64.zip，里面只有「万能格式转换.app」
#   用法：scripts/build-mac.sh 1.1.0
# 发版时由 .github/workflows/release.yml 在 macOS 上调用。需要 Xcode 命令行工具（clang、codesign）。
# 需要先运行 scripts/fetch-tools-mac.sh 下载转换引擎（tools/ 已经存在时跳过）。
#
# .app 里的结构（代码都在 Contents/MacOS 和 Contents/Frameworks，签名才能通过）：
#   Contents/MacOS/万能格式转换    主程序（AppKit 界面 + 命令行模式）
#   Contents/MacOS/ffmpeg、ffprobe、magick
#   Contents/Frameworks/*.dylib    PDFium 和 ImageMagick 用到的库
#   Contents/Resources/magick/     ImageMagick 的配置文件
#   Contents/Resources/licenses/   各组件的许可证
# 没有苹果开发者账号，所以只做 ad-hoc 签名（Apple 芯片上必须有签名才能运行），没有公证。
set -euo pipefail
cd "$(dirname "$0")/.."

VER="${1:-dev}"
APP="万能格式转换"
ZIP="OmniConvert-mac-arm64.zip"
[ -x tools/ffmpeg/ffmpeg ] && [ -x tools/magick/bin/magick ] && [ -f tools/pdfium/libpdfium.dylib ] || bash scripts/fetch-tools-mac.sh

PLIST_VER="$VER"
[[ "$VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || PLIST_VER=0.0.0

rm -rf dist
mkdir -p dist
app="dist/$APP.app"
C="$app/Contents"
mkdir -p "$C/MacOS" "$C/Frameworks" "$C/Resources/magick" "$C/Resources/licenses" "$C/Resources/zh-Hans.lproj"

# 主程序：界面用到 AppKit（Objective-C），所以要开 cgo。Go 1.26 本身最低支持 macOS 12。
export MACOSX_DEPLOYMENT_TARGET=12.0
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
	CGO_CFLAGS="-mmacosx-version-min=12.0 -O2" CGO_LDFLAGS="-mmacosx-version-min=12.0" \
	go build -trimpath -ldflags "-s -w -X main.version=$VER" -o "$C/MacOS/$APP" .

# 转换引擎
cp tools/ffmpeg/ffmpeg tools/ffmpeg/ffprobe "$C/MacOS/"
cp tools/magick/bin/magick "$C/MacOS/"
cp tools/magick/lib/*.dylib "$C/Frameworks/"
cp tools/pdfium/libpdfium.dylib "$C/Frameworks/"
# magick 原来到 ../lib 找库，在 .app 里改成 ../Frameworks
install_name_tool -add_rpath @executable_path/../Frameworks "$C/MacOS/magick" 2> /dev/null
install_name_tool -delete_rpath @loader_path/../lib/ "$C/MacOS/magick" 2> /dev/null || true
cp tools/magick/config/* "$C/Resources/magick/"

# 许可证
cp LICENSE "$C/Resources/licenses/万能格式转换-LICENSE.txt"
cp tools/ffmpeg/LICENSE.txt "$C/Resources/licenses/FFmpeg-LICENSE.txt"
cp tools/pdfium/LICENSE "$C/Resources/licenses/PDFium-LICENSE.txt"
mkdir -p "$C/Resources/licenses/ImageMagick"
cp -R tools/magick/licenses/. "$C/Resources/licenses/ImageMagick/"

# 图标：从 assets/icon-mac.svg 画出各种尺寸，再打包成 icns
iconset="$(mktemp -d)/AppIcon.iconset"
mkdir -p "$iconset"
MAGICK_CONFIGURE_PATH="$PWD/tools/magick/config" FONTCONFIG_FILE="$PWD/tools/magick/config/fonts.conf" \
	tools/magick/bin/magick -background none -density 192 assets/icon-mac.svg -resize 1024x1024 "PNG32:$iconset/icon_512x512@2x.png"
for s in 16 32 128 256 512; do
	sips -z $s $s "$iconset/icon_512x512@2x.png" --out "$iconset/icon_${s}x${s}.png" > /dev/null
	d=$((s * 2))
	[ $s = 512 ] || sips -z $d $d "$iconset/icon_512x512@2x.png" --out "$iconset/icon_${s}x${s}@2x.png" > /dev/null
done
iconutil -c icns "$iconset" -o "$C/Resources/AppIcon.icns"
rm -rf "$(dirname "$iconset")"

# 最低系统版本：取所有程序和库里要求最高的那个（只会比 12.0 高，不会低）
minos=12.0
while IFS= read -r f; do
	v=$(otool -l "$f" 2> /dev/null | awk '/LC_BUILD_VERSION/{b=1} b&&/minos/{print $2; exit}')
	[ -z "$v" ] && v=$(otool -l "$f" 2> /dev/null | awk '/LC_VERSION_MIN_MACOSX/{b=1} b&&/version/{print $2; exit}')
	[ -n "$v" ] || continue
	if [ "$(printf '%s\n%s\n' "$minos" "$v" | sort -t. -k1,1n -k2,2n | tail -1)" != "$minos" ]; then
		minos=$v
		echo "  $f 要求 macOS $v"
	fi
done < <(find "$C/MacOS" "$C/Frameworks" -type f)
echo "最低系统版本：macOS $minos"
echo "$minos" > dist/minos.txt

go run ./scripts/macplist -version "$PLIST_VER" -min "$minos" > "$C/Info.plist"
plutil -lint "$C/Info.plist"
printf 'APPL????' > "$C/PkgInfo"
cat > "$C/Resources/zh-Hans.lproj/InfoPlist.strings" << TXT
CFBundleName = "$APP";
CFBundleDisplayName = "$APP";
NSHumanReadableCopyright = "© 2026 haoawake · MIT License";
TXT

# 签名：先签里面的每个程序和库，再签整个 .app
find "$C/Frameworks" "$C/MacOS" -type f ! -name "$APP" -print0 | xargs -0 codesign --force --sign - --timestamp=none
codesign --force --deep --sign - --timestamp=none "$app"
codesign --verify --deep --strict --verbose=2 "$app"

ditto -c -k --sequesterRsrc --keepParent "$app" "dist/$ZIP"
(cd dist && shasum -a 256 "$ZIP" > SHA256SUMS-mac.txt)
du -sh "$app" "dist/$ZIP"
