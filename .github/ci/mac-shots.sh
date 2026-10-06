#!/usr/bin/env bash
# 开发用：让打包好的程序按步骤操作界面，再把窗口自己画成 PNG（-shot 模式，不需要「屏幕录制」权限）
#   用法：mac-shots.sh 万能格式转换.app 测试文件夹 输出文件夹
set -uo pipefail
app=$1
files=$2
out=$3
mkdir -p "$out"
BIN="$app/Contents/MacOS/万能格式转换"
f() { echo "$files/$1"; }

shot() { # shot 名字 步骤 [外观 light|dark]
	local name=$1 steps=$2 look=${3:-light}
	echo "== $name ($look)"
	OMNI_SHOT_APPEARANCE=$look perl -e 'alarm shift; exec @ARGV' 120 "$BIN" -shot "$out/$name.png" -steps "$steps" > "$out/$name.log" 2>&1
	echo "  exit $? $(ls -l "$out/$name.png" 2>/dev/null)"
}

shot 01-image-empty "page:0"
shot 02-image-queued "page:0,add:$(f '照片 原图.jpg')|$(f '苹果照片.heic')|$(f '截图.png')|$(f '花纹 大图.jpg'),target:img:jpg,set:resize=box,set:w=295,set:h=413,set:fit=cover,set:sizepreset=204800"
shot 03-image-done "page:0,add:$(f '截图.png')|$(f '苹果照片.heic')|$(f '说明.md'),target:img:webp,start,waitdone"
shot 04-video "page:1,add:$(f '视频 片段.mov'),target:vid:mp4,set:res=custom,set:w=1280,set:h=720,set:sizepreset=custom,set:sizenum=8,set:sizeunit=MB"
shot 05-video-running "page:1,add:$(f '视频 片段.mov'),target:vid:mkv,set:vcodec=h265,start,wait:1500"
shot 06-audio "page:2,add:$(f '声音.wav')|$(f '视频 片段.mov'),target:aud:mp3"
shot 07-doc "page:3,add:$(f '笔记.txt')|$(f '表格.csv')|$(f '说明.md'),target:doc:pdf"
shot 08-pdf-split "page:4,add:$(f '文档 A.pdf')|$(f '截图 等2张图片.pdf'),target:pdf:split,set:split=ranges,set:ranges=1-2"
shot 09-pdf-done-failed "page:4,add:$(f '文档 A.pdf')|$(f '笔记.txt')|$(f '文档 A_加密.pdf'),target:pdf:png,start,waitdone,select:1"
shot 10-dark-image "page:0,add:$(f '照片 原图.jpg')|$(f '截图.png'),target:img:avif" dark
shot 11-dark-done "page:4,add:$(f '文档 A.pdf')|$(f '文档 A_加密.pdf'),target:pdf:jpg,start,waitdone" dark
shot 12-small-window "size:1000x660,page:1,add:$(f '视频 片段.mov'),target:vid:gif"
ls -l "$out"
