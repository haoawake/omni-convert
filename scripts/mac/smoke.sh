#!/usr/bin/env bash
# 用打包好的 .app 里的程序（命令行模式）把主要功能各转一遍，检查结果对不对。
#   用法：scripts/mac/smoke.sh dist/万能格式转换.app [工作文件夹]
# 只用 .app 里自带的东西：环境变量清空（PATH 只留系统目录），不依赖 Homebrew。
# Word / Excel / PPT 相关的几项需要装了 LibreOffice（/Applications/LibreOffice.app），没有时跳过。
set -uo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
app=$(cd "$1" && pwd)
work=${2:-$(mktemp -d)}
mkdir -p "$work"
cd "$work"
C="$app/Contents"
BIN="$C/MacOS/万能格式转换"
FFMPEG="$C/MacOS/ffmpeg"
FFPROBE="$C/MacOS/ffprobe"

pass=0
fail=0
failed=()

# 在干净的环境里运行（没有 Homebrew、没有 DYLD_* 变量）
clean() { env -i HOME="$HOME" TMPDIR="${TMPDIR:-/tmp}" PATH=/usr/bin:/bin:/usr/sbin:/sbin LANG=zh_CN.UTF-8 "$@"; }
conv() { clean "$BIN" -q "$@"; }
magick() { clean MAGICK_CONFIGURE_PATH="$C/Resources/magick" "$C/MacOS/magick" "$@"; }
probe() { clean "$FFPROBE" -v error "$@"; }
ff() { clean "$FFMPEG" -hide_banner -loglevel error -y "$@"; }
kind() { magick identify -quiet -format '%m\n' "$1" | head -1; }
dims() { magick identify -quiet -format '%w %h\n' "$1" | head -1; }
frames() { magick identify -quiet "$1" | wc -l | tr -d ' '; }
vcodec() { probe -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 "$1"; }
acodec() { probe -select_streams a:0 -show_entries stream=codec_name -of csv=p=0 "$1"; }
fsize() { stat -f %z "$1"; }
export C BIN FFMPEG FFPROBE
export -f clean conv magick probe ff kind dims frames vcodec acodec fsize

# check 名字 '命令'：命令（交给 bash -c）成功就算通过，失败时显示输出的最后几行
check() {
	local name=$1 log
	log="log-$((pass + fail)).txt"
	if bash -c "$2" > "$log" 2>&1; then
		pass=$((pass + 1))
		echo "  ✓ $name"
	else
		fail=$((fail + 1))
		failed+=("$name")
		echo "  ✗ $name"
		sed 's/^/      /' "$log" | tail -15
	fi
}

echo "== 准备测试文件（${work}）"
ff -f lavfi -i "mandelbrot=size=3000x2000" -frames:v 1 -q:v 2 "照片 原图.jpg"
magick -size 3000x2000 plasma:fractal -quality 95 "花纹 大图.jpg"
ff -f lavfi -i "testsrc2=size=800x600" -frames:v 1 "截图.png"
ff -f lavfi -i "testsrc2=size=1280x720:rate=30" -f lavfi -i "sine=frequency=440:sample_rate=48000" \
	-t 12 -c:v libx264 -pix_fmt yuv420p -c:a aac -b:a 128k -shortest "视频 片段.mov"
ff -f lavfi -i "sine=frequency=330:duration=8" -ac 2 "声音.wav"
if sips -s format heic "照片 原图.jpg" --out "苹果照片.heic" > /dev/null 2>&1 && [ -s "苹果照片.heic" ]; then
	echo "  （HEIC 用 sips 生成）"
else
	magick "照片 原图.jpg" -resize 1500x1000 "苹果照片.heic"
	echo "  （HEIC 用 ImageMagick 生成）"
fi
cp "$repo/internal/pdf/testdata/中文 测试文档.pdf" "文档 A.pdf"
printf '标题,数量,单价\n苹果,3,5.5\n香蕉,10,2\n' > "表格.csv"
printf '# 标题\n\n一段**中文**说明。\n\n- 第一项\n- 第二项\n' > "说明.md"
printf '第一行中文\n第二行 English\n' > "笔记.txt"
ls -l

echo "== 图片"
check "HEIC → JPG" 'conv -to img:jpg 苹果照片.heic && [ "$(kind 苹果照片.jpg)" = JPEG ]'
check "PNG → WebP" 'conv -to img:webp 截图.png && [ "$(kind 截图.webp)" = WEBP ]'
check "PNG → AVIF" 'conv -to img:avif 截图.png && kind 截图.avif | grep -q AVIF'
check "PNG → JXL" 'conv -to img:jxl 截图.png && kind 截图.jxl | grep -q JXL'
check "JPG → PNG / TIFF / ICO / GIF / BMP / TGA" 'for f in png tiff ico gif bmp tga; do conv -to img:$f "花纹 大图.jpg" || exit 1; done'
check "HEIC → 原格式（存成 JPG）并去掉拍摄信息" 'mkdir -p same && conv -to img:same -o same -set strip=1 苹果照片.heic && [ "$(kind same/苹果照片.jpg)" = JPEG ]'
check "裁成一寸照 295×413" 'mkdir -p crop && conv -to img:jpg -o crop -set resize=box -set w=295 -set h=413 -set fit=cover "照片 原图.jpg" && d=$(dims "crop/照片 原图.jpg") && echo "$d" && [ "$d" = "295 413" ]'
check "压到 200 KB 以内" 'mkdir -p small && conv -to img:jpg -o small -set size=204800 "花纹 大图.jpg" && s=$(fsize "small/花纹 大图.jpg") && echo $s && [ $s -le 204800 ] && [ $s -gt 50000 ]'
check "图片合成 PDF" 'conv -to img:pdfmerge 截图.png "照片 原图.jpg" && head -c 5 "截图 等2张图片.pdf" | grep -q %PDF-'

echo "== 视频"
check "视频 → MP4（压到 1 MB 以内）" 'mkdir -p vsmall && conv -to vid:mp4 -o vsmall -set size=1048576 "视频 片段.mov" && s=$(fsize "vsmall/视频 片段.mp4") && echo $s && [ $s -le 1048576 ] && [ "$(vcodec "vsmall/视频 片段.mp4")" = h264 ]'
check "视频 → H.265 MKV（480P）" 'conv -to vid:mkv -set vcodec=h265 -set res=854x480 "视频 片段.mov" && [ "$(vcodec "视频 片段.mkv")" = hevc ]'
check "视频 → WEBM（VP9）" 'conv -to vid:webm -set end=4 "视频 片段.mov" && [ "$(vcodec "视频 片段.webm")" = vp9 ]'
check "视频 → AV1 MP4" 'mkdir -p av1 && conv -to vid:mp4 -o av1 -set vcodec=av1 -set end=2 "视频 片段.mov" && [ "$(vcodec "av1/视频 片段.mp4")" = av1 ]'
check "视频 → AVI / WMV / MPG / FLV / 3GP" 'for f in avi wmv mpg flv 3gp; do conv -to vid:$f -set end=2 "视频 片段.mov" || exit 1; done'
check "视频 → GIF 动图" 'conv -to vid:gif -set end=3 "视频 片段.mov" && n=$(frames "视频 片段.gif") && echo $n && [ $n -gt 10 ]'
check "视频 → WEBP 动图" 'conv -to vid:webp -set end=2 "视频 片段.mov" && [ -s "视频 片段.webp" ]'

echo "== 音频"
check "WAV → MP3" 'conv -to aud:mp3 声音.wav && [ "$(acodec 声音.mp3)" = mp3 ]'
check "视频里提取声音 → M4A" 'conv -to aud:m4a "视频 片段.mov" && [ "$(acodec "视频 片段.m4a")" = aac ]'
check "WAV → FLAC / OGG / OPUS / AIFF / WMA / AC3 / 铃声" 'for f in flac ogg opus aiff wma ac3 m4r; do conv -to aud:$f 声音.wav || exit 1; done'
check "WAV → AMR" 'conv -to aud:amr 声音.wav && [ -s 声音.amr ]'

echo "== PDF"
check "合并" 'conv -to pdf:merge "文档 A.pdf" "截图 等2张图片.pdf" && [ -s "文档 A等2个文件_合并.pdf" ]'
check "  合并结果每页转一张图" 'conv -to pdf:png -set dpi=96 "文档 A等2个文件_合并.pdf" && ls "文档 A等2个文件_合并" && [ $(ls "文档 A等2个文件_合并" | wc -l) -ge 3 ]'
check "拆分（每页一个）" 'conv -to pdf:split "截图 等2张图片.pdf" && [ $(ls "截图 等2张图片_拆分" | wc -l) -eq 2 ]'
check "压缩（推荐）" 'conv -to pdf:compress "文档 A.pdf" && [ -s "文档 A_压缩.pdf" ]'
check "压缩（强力）" 'conv -to pdf:compress -set level=strong "截图 等2张图片.pdf" && [ -s "截图 等2张图片_压缩.pdf" ]'
check "转 JPG 图片" 'conv -to pdf:jpg "文档 A.pdf"'
check "转长图" 'conv -to pdf:long "文档 A.pdf" && [ "$(kind "文档 A_长图.jpg")" = JPEG ]'
check "转 TXT（中文）" 'conv -to pdf:txt "文档 A.pdf" && cat "文档 A.txt" | head -5 && [ -s "文档 A.txt" ]'
check "转 PPT" 'conv -to pdf:pptx "文档 A.pdf" && unzip -l "文档 A.pptx" | grep -q ppt/slides'
check "加密再解密" 'conv -to pdf:encrypt -set newpw=abc123 "文档 A.pdf" && conv -to pdf:decrypt -set password=abc123 "文档 A_加密.pdf" && [ -s "文档 A_加密_解密.pdf" ]'
check "旋转" 'conv -to pdf:rotate "文档 A.pdf" && [ -s "文档 A_旋转.pdf" ]'

echo "== 文档"
check "CSV → XLSX（不用 Office）" 'conv -to doc:xlsx 表格.csv && unzip -l 表格.xlsx | grep -q xl/workbook.xml'
check "Markdown → HTML（不用 Office）" 'conv -to doc:html 说明.md && grep -q "<strong>中文</strong>" 说明.html'
check "Markdown → PDF（浏览器或 LibreOffice）" 'conv -to doc:pdf 说明.md && head -c 5 说明.pdf | grep -q %PDF-'
if [ -x /Applications/LibreOffice.app/Contents/MacOS/soffice ]; then
	check "TXT → DOCX（LibreOffice）" 'conv -to doc:docx 笔记.txt && unzip -l 笔记.docx | grep -q word/document.xml'
	check "DOCX → PDF（LibreOffice）" 'conv -to doc:pdf 笔记.docx && head -c 5 笔记.pdf | grep -q %PDF-'
	check "XLSX → PDF（LibreOffice）" 'conv -to doc:pdf 表格.xlsx && head -c 5 表格.pdf | grep -q %PDF-'
	check "PPTX → PDF（LibreOffice）" 'mkdir -p ppt && conv -to doc:pdf -o ppt "文档 A.pptx" && head -c 5 "ppt/文档 A.pdf" | grep -q %PDF-'
	check "DOCX → 长图" 'conv -to doc:long 笔记.docx && [ "$(kind 笔记_长图.jpg)" = JPEG ]'
	check "XLSX → CSV" 'mkdir -p csv && conv -to doc:csv -o csv 表格.xlsx && grep -q 苹果 csv/表格.csv'
	check "PDF → Word（LibreOffice）" 'conv -to pdf:docx "文档 A.pdf" && unzip -l "文档 A.docx" | grep -q word/document.xml'
else
	echo "  （没有 LibreOffice，跳过 Word / Excel / PPT）"
	check "PDF → Word（没有 LibreOffice 时只提取文字）" 'conv -to pdf:docx "文档 A.pdf" && unzip -l "文档 A.docx" | grep -q word/document.xml'
fi

echo
ls -lR | head -150
echo
echo "通过 $pass 项，失败 $fail 项"
for f in "${failed[@]+"${failed[@]}"}"; do echo "  失败：$f"; done
[ $fail = 0 ]
