# 更新记录

## v1.1.0

- **新增 macOS 版**（Apple 芯片，macOS 13 或更新）：下载 `OmniConvert-mac-arm64.zip`，解压后把「万能格式转换」拖进「应用程序」。
  - 原生的 Mac 界面（支持深色模式），和 Windows 版一样分图片、视频、音频、文档、PDF 五页；拖进文件、`⌘V` 粘贴拷贝的文件、在访达里「打开方式 → 万能格式转换」、拖到程序坞图标上都行。
  - FFmpeg、ImageMagick、PDFium 都装在程序里，不用另外安装；HEIC、RAW、AVIF、JPEG XL 等图片格式和 Windows 版一样能用。
  - 视频可以用苹果芯片的硬件编码（H.264 / H.265）加速。
  - Word、Excel、PPT 相关的转换需要安装免费的 LibreOffice（Mac 上的 Microsoft Office 不能在后台自动转换）；网页、Markdown 转 PDF 用电脑上的 Edge 或 Chrome。
  - Mac 版暂时不能转成 AMR（没有可以分发的 AMR 编码器）。
- PPT 转视频：没有 Microsoft PowerPoint 时（Mac，或者 Windows 上只装了 WPS、LibreOffice）也能转了，每一页做成一个画面，按「每页停留」的秒数自动翻页。

## v1.0.0

第一个版本。

- **图片**：读得懂 iPhone 的 HEIC、单反 RAW、PSD、SVG 等几十种格式，转 JPG、PNG、WEBP、AVIF、JXL、BMP、GIF、TIFF、ICO、TGA、PDF。
  改尺寸、裁成固定像素（一寸照、二寸照、头像、壁纸等常用尺寸一键选）、压到指定大小、去掉拍摄信息、多张图片合成一个 PDF。
- **视频**：MP4、MKV、MOV、AVI、WEBM、FLV、WMV、M4V、TS、MPG、3GP 互转，转 GIF / WEBP 动图。
  H.264 / H.265 / AV1，压到指定大小（两遍编码，压不下时自动降分辨率），改分辨率、帧率，截取片段，去掉声音，显卡加速。
- **音频**：MP3、M4A、AAC、WAV、FLAC、OGG、OPUS、WMA、AIFF、AC3、AMR、苹果铃声 M4R，能直接从视频里提取声音。
- **文档**：调用电脑上的 Office（或 WPS、LibreOffice）转换 Word、Excel、PPT；PPT 转视频；TXT、Markdown、网页转 PDF / Word；
  CSV 和 Excel 互转不需要 Office；任何文档转成图片或一张长图。
- **PDF**：转 Word、Excel、PPT、JPG、PNG、长图、TXT；合并、拆分、压缩、加密、解密、旋转。
- 拖进文件或文件夹自动分到对应的页；`Ctrl+V` 粘贴复制的文件；可以加到右键菜单，选中一堆文件也只开一个窗口。
- 转换结果默认和原文件放在一起，永远不覆盖原文件；失败的任务不会留下转了一半的文件。
- 转换时不让电脑睡眠，任务栏按钮显示总进度。
