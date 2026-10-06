#!/usr/bin/env bash
# 开发用：检查下载的 macOS 引擎有哪些编码器、格式，依赖了哪些库（结果写到标准输出）
set -uo pipefail
cd "$(dirname "$0")/../.."
T=tools
echo "== sw_vers"; sw_vers; uname -a
echo "== sizes"; du -sh $T/* $T/magick/lib; ls -l $T/ffmpeg $T/pdfium; ls $T/magick/lib | wc -l
echo "== ffmpeg -version"; $T/ffmpeg/ffmpeg -hide_banner -version | head -3
echo "== encoders we use"
enc=$($T/ffmpeg/ffmpeg -hide_banner -encoders 2>/dev/null)
for e in libx264 libx265 libsvtav1 libaom-av1 libvpx-vp9 libwebp_anim libwebp libmp3lame libopus libvorbis aac aac_at flac wmav2 mp2 ac3 mpeg4 wmv2 mpeg2video pcm_s16le pcm_s16be gif libopencore_amrnb h264_videotoolbox hevc_videotoolbox prores_videotoolbox; do
  if echo "$enc" | grep -qE "^ [A-Z.]{6} $e( |$)"; then echo "  ok $e"; else echo "  MISSING $e"; fi
done
echo "== filters"
fil=$($T/ffmpeg/ffmpeg -hide_banner -filters 2>/dev/null)
for f in zscale tonemap palettegen paletteuse loudnorm scale pad crop fps setsar split format; do
  if echo "$fil" | grep -qE " $f +[A-Z|N]"; then echo "  ok $f"; else echo "  MISSING $f"; fi
done
echo "== muxers"
mux=$($T/ffmpeg/ffmpeg -hide_banner -muxers 2>/dev/null)
for m in mp4 mov matroska webm flv mpegts avi asf vob 3gp 3g2 gif webp image2 ipod adts mp3 wav flac aiff ogg opus ac3 amr null; do
  if echo "$mux" | grep -qE "^ [ E]+ $m "; then echo "  ok $m"; else echo "  MISSING $m"; fi
done
echo "== otool ffmpeg"; otool -L $T/ffmpeg/ffmpeg
echo "== videotoolbox test"
$T/ffmpeg/ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=640x360:rate=30 -frames:v 10 -c:v h264_videotoolbox -pix_fmt nv12 -q:v 60 -f null - && echo "  h264_videotoolbox works" || echo "  h264_videotoolbox fails"
$T/ffmpeg/ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=640x360:rate=30 -frames:v 10 -c:v hevc_videotoolbox -pix_fmt nv12 -q:v 60 -f null - && echo "  hevc_videotoolbox works" || echo "  hevc_videotoolbox fails"
echo "== magick"
export MAGICK_CONFIGURE_PATH=$PWD/$T/magick/config FONTCONFIG_FILE=$PWD/$T/magick/config/fonts.conf
$T/magick/bin/magick -version
$T/magick/bin/magick -list format | grep -iE " (HEIC|HEIF|AVIF|WEBP|JXL|TIFF|DNG|CR2|CR3|NEF|ARW|SVG|PSD|JP2|EXR|ICO|TGA|QOI|DDS|XCF|PCX|HDR|BMP|GIF|PNG|JPEG|RAW|MSVG|RSVG|EMF|WMF)\*? "
$T/magick/bin/magick -list delegate | head -20
$T/magick/bin/magick -list policy | head -20
echo "== magick test HEIC/AVIF/JXL write"
cd "$(mktemp -d)"
$OLDPWD/$T/magick/bin/magick -size 300x200 gradient:red-blue a.png && for f in heic avif jxl webp tif; do $OLDPWD/$T/magick/bin/magick a.png out.$f && echo "  wrote $f $(stat -f %z out.$f) $($OLDPWD/$T/magick/bin/magick identify -format %m out.$f)" || echo "  FAIL $f"; done
echo '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><rect width="200" height="100" fill="#09f"/><text x="20" y="60" font-size="30">中文 Abc</text></svg>' > t.svg
time $OLDPWD/$T/magick/bin/magick -density 96 t.svg svg.png && echo "  svg ok $($OLDPWD/$T/magick/bin/magick identify -format '%m %wx%h' svg.png)"
echo "== sips heic"; sips -s format heic a.png --out s.heic && ls -l s.heic
echo "== afconvert AMR"
$OLDPWD/$T/ffmpeg/ffmpeg -hide_banner -loglevel error -f lavfi -i sine=frequency=440:duration=3 -ar 8000 -ac 1 -c:a pcm_s16le t.wav
afconvert -hf 2>&1 | grep -i -A1 amr
afconvert -f amrf -d samr t.wav t.amr && ls -l t.amr && head -c 6 t.amr | xxd && $OLDPWD/$T/ffmpeg/ffprobe -v error -show_entries stream=codec_name,sample_rate -of csv t.amr || echo "  afconvert AMR failed"
cd "$OLDPWD"
echo "== minos of everything"
for f in $T/ffmpeg/ffmpeg $T/ffmpeg/ffprobe $T/magick/bin/magick $T/magick/lib/*.dylib $T/pdfium/libpdfium.dylib; do
  v=$(otool -l "$f" | awk '/LC_BUILD_VERSION/{b=1} b&&/minos/{print $2; exit}')
  echo "  $v $(basename $f)"
done | sort -V | tail -15
echo "== non-rpath deps of magick libs"
for f in $T/magick/bin/magick $T/magick/lib/*.dylib; do otool -L "$f" | tail -n +2 | awk '{print $1}' | grep -vE "^(@rpath|/usr/lib|/System)" | sed "s|^|  $(basename $f): |"; done
echo "== system libs used by magick closure"
for f in $T/magick/lib/*.dylib; do otool -L "$f" | tail -n +2 | awk '{print $1}' | grep -E "^(/usr/lib|/System)"; done | sort | uniq -c | sort -rn
echo "== browsers / office"
ls -d /Applications/*Chrome* /Applications/*Edge* /Applications/*Chromium* /Applications/LibreOffice.app 2>/dev/null
echo "== lock"; cat $T/magick/imagemagick-osx-arm64.lock | head -100
