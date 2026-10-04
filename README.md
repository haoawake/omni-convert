<p align="center"><img src="icon.png" width="88" alt="OmniConvert icon"></p>

# OmniConvert (万能格式转换)

One small toolbox for all the formats you run into: images, video, audio, Word / Excel / PowerPoint and PDF. Convert between all the common formats, crop pictures to an exact pixel size (e.g. passport photos), squeeze photos and videos under a size limit, turn PDFs into Word, merge / split / compress / encrypt PDFs. A native Windows app; everything runs locally and nothing is uploaded.

[中文说明](README.zh-CN.md) · [Download](https://github.com/haoawake/omni-convert/releases/latest)

![OmniConvert: pick a category on the left, a target format on top, files on the right](docs/screenshot.png)

## What it does

| Category | Convert to | Also |
|---|---|---|
| **Images** | JPG, PNG, WEBP, AVIF, JXL, BMP, GIF, TIFF, ICO, TGA, PDF | Reads iPhone HEIC, camera RAW (CR2/CR3/NEF/ARW/DNG…), PSD, SVG and dozens more; resize, **crop to an exact size** (fill / fit / pad / stretch), **compress below a target size**, strip EXIF/GPS, combine images into one PDF |
| **Video** | MP4, MKV, MOV, AVI, WEBM, FLV, WMV, M4V, TS, MPG, 3GP, animated GIF, animated WEBP | H.264 / H.265 / AV1, **fit under a target size** (two-pass), change resolution (incl. portrait, square, custom), trim, change frame rate, remove audio, NVIDIA / AMD / Intel GPU encoding |
| **Audio** | MP3, M4A, AAC, WAV, FLAC, OGG, OPUS, WMA, AIFF, AC3, AMR, iPhone ringtone (M4R) | **Extract audio from videos**, bitrate / sample rate / channels, target size, trim, loudness normalization |
| **Documents** | PDF, DOCX, DOC, RTF, ODT, TXT, HTML, XLSX, XLS, CSV, ODS, PPTX, PPT, ODP, images, long image, video | Word / Excel / PowerPoint / TXT / Markdown / HTML conversions, **PowerPoint to MP4**, any document to page images or one long image |
| **PDF** | Word, Excel, PowerPoint, JPG, PNG, long image, TXT | **Merge, split** (per page / every N pages / page ranges), **compress** (lossless / recommended / strong), encrypt, decrypt, rotate |

## Usage

1. Download `OmniConvert-win-x64.zip` from [Releases](https://github.com/haoawake/omni-convert/releases/latest) and extract it somewhere permanent. Windows 10 or 11.
2. Run `万能格式转换.exe`. If SmartScreen says "Windows protected your PC", click *More info* → *Run anyway*.
3. Drag files or folders onto the window (or *Add files*, or `Ctrl+V` files copied in Explorer). Files are sorted into the right category automatically.
4. Pick a target, adjust the settings, press *Start*. Results go next to the originals by default and **never overwrite anything** (name clashes get a ` (1)` suffix).

Keep the `tools` folder (FFmpeg, ImageMagick, PDFium) next to the program.

Word / Excel / PowerPoint conversions use Microsoft Office, WPS or LibreOffice if one of them is installed (silently, in the background). CSV ↔ XLSX and Markdown → HTML work without Office.

A context-menu entry ("Open with OmniConvert") can be added from the top-right button; selecting many files opens a single window.

## Building

Requires Go 1.26+ and 7z.

```bash
bash scripts/fetch-tools.sh   # downloads FFmpeg, ImageMagick and PDFium into tools/
go test ./...
bash scripts/build.sh 1.0.0   # writes dist/OmniConvert-win-x64.zip
```

Command line: `万能格式转换.exe -list` lists every conversion; `万能格式转换.exe -to img:jpg -set size=204800 photo.heic` converts directly.

## License

[MIT](LICENSE). Bundled components keep their own licenses: FFmpeg (GPL, source at ffmpeg.org), ImageMagick (ImageMagick License), PDFium (BSD), pdfcpu (Apache 2.0); license files are in the `tools` subfolders.
