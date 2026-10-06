package catalog

import (
	"runtime"
	"strconv"

	"github.com/haoawake/omni-convert/internal/conv"
)

// GPUAvailable 由 bind.go 设置：电脑上有能用的显卡编码器时返回 true（显示「用显卡加速」）
var GPUAvailable = func() bool { return false }

func is(key string, values ...string) func(o conv.Options) bool {
	return func(o conv.Options) bool {
		v := o[key]
		for _, x := range values {
			if v == x {
				return true
			}
		}
		return false
	}
}

func not(f func(o conv.Options) bool) func(o conv.Options) bool {
	return func(o conv.Options) bool { return !f(o) }
}

func choices(pairs ...string) []Choice {
	var out []Choice
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Choice{pairs[i], pairs[i+1]})
	}
	return out
}

// ---------------------------------------------------------------- 公用的几行

func sizeRow(presets ...string) Row {
	ch := []Choice{{"0", "不限制"}}
	for _, p := range presets {
		n, _ := conv.ParseSize(p)
		ch = append(ch, Choice{strconv.FormatInt(n, 10), p})
	}
	ch = append(ch, Choice{"custom", "自定义…"})
	custom := is(keySizePreset, "custom")
	return Row{
		Label: "文件大小",
		Fields: []Field{
			{Key: keySizePreset, Type: Select, Choices: ch, Default: "0", Width: 110},
			{Key: keySizeNum, Type: Number, Width: 70, Placeholder: "数值", ShowIf: custom},
			{Key: keySizeUnit, Type: Seg, Choices: choices("KB", "KB", "MB", "MB"), Default: "MB", ShowIf: custom},
		},
		Note: "压到不超过这个大小",
	}
}

func trimRow() Row {
	return Row{
		Label: "截取",
		Fields: []Field{
			{Key: conv.OptStart, Type: Text, Width: 90, Placeholder: "开始 0:00"},
			{Type: Static, Label: "到"},
			{Key: conv.OptEnd, Type: Text, Width: 90, Placeholder: "结束 末尾"},
		},
		Note: "只要中间一段时填，比如 0:10 到 1:30",
	}
}

func pagesField() Field {
	return Field{Key: conv.OptPages, Type: Text, Width: 150, Placeholder: "全部，或者 1-3,5"}
}

func dpiRow() Row {
	return Row{Label: "清晰度", Fields: []Field{
		{Key: conv.OptDPI, Type: Seg, Choices: choices("96", "普通", "150", "清晰", "200", "高清", "300", "超清"), Default: "150"},
	}}
}

func longRow() Row {
	return Row{Label: "长图宽度", Fields: []Field{
		{Key: conv.OptLongW, Type: Seg, Choices: choices("720", "720", "1080", "1080", "1440", "1440", "2160", "2160"), Default: "1080", Unit: "像素"},
	}, Note: "手机上看选 1080 就够了"}
}

func passwordRow(label, placeholder string) Row {
	return Row{Label: label, Fields: []Field{
		{Key: conv.OptPassword, Type: Password, Width: 180, Placeholder: placeholder},
	}}
}

// ---------------------------------------------------------------- 图片

var imagePresets = []struct{ name, w, h string }{
	{"一寸照 295×413", "295", "413"},
	{"小二寸 413×531", "413", "531"},
	{"二寸照 413×579", "413", "579"},
	{"头像 512×512", "512", "512"},
	{"方形 1080×1080", "1080", "1080"},
	{"图标 256×256", "256", "256"},
	{"横屏 1920×1080", "1920", "1080"},
	{"横屏 1280×720", "1280", "720"},
	{"手机壁纸 1080×1920", "1080", "1920"},
	{"小红书 1242×1660", "1242", "1660"},
	{"公众号封面 900×383", "900", "383"},
	{"A4 打印 2480×3508", "2480", "3508"},
}

func imageSizeRows() []Row {
	box := is(conv.OptResize, "box")
	var presets []Choice
	for _, p := range imagePresets {
		presets = append(presets, Choice{p.w + "x" + p.h, p.name})
	}
	return []Row{{
		Label: "尺寸",
		Fields: []Field{
			{Key: conv.OptResize, Type: Select, Default: "none", Width: 110,
				Choices: choices("none", "保持原样", "box", "指定宽高", "percent", "按百分比", "long", "限制长边")},
			{Key: conv.OptWidth, Type: Number, Width: 64, Placeholder: "宽", ShowIf: box},
			{Type: Static, Label: "×", ShowIf: box},
			{Key: conv.OptHeight, Type: Number, Width: 64, Placeholder: "高", Unit: "像素", ShowIf: box},
			{Key: conv.OptFit, Type: Select, Default: "cover", Width: 110, ShowIf: box,
				Choices: choices("cover", "裁剪填满", "contain", "等比缩放", "pad", "留白填充", "stretch", "拉伸")},
			{Key: conv.OptPercent, Type: Number, Width: 64, Default: "50", Unit: "%", ShowIf: is(conv.OptResize, "percent")},
			{Key: conv.OptLong, Type: Number, Width: 70, Default: "1920", Unit: "像素", ShowIf: is(conv.OptResize, "long")},
			{Key: keyPreset, Type: Select, Width: 120, Choices: append([]Choice{{"", "常用尺寸…"}}, presets...),
				Apply: func(o conv.Options, v string) {
					for _, p := range imagePresets {
						if p.w+"x"+p.h == v {
							o[conv.OptResize], o[conv.OptWidth], o[conv.OptHeight], o[conv.OptFit] = "box", p.w, p.h, "cover"
						}
					}
				}},
		},
	},
		fitNote("cover", "正好是这个尺寸，多出来的部分从中间裁掉"),
		fitNote("contain", "完整保留画面，按比例缩小到不超过这个尺寸"),
		fitNote("pad", "完整保留画面，四周补白边凑成这个尺寸"),
		fitNote("stretch", "直接拉伸成这个尺寸，画面可能变形"),
	}
}

func codecNote(codec, note string) Row {
	return Row{ShowIf: is(conv.OptVCodec, codec), Note: note}
}

func fitNote(fit, note string) Row {
	return Row{ShowIf: func(o conv.Options) bool { return o[conv.OptResize] == "box" && o[conv.OptFit] == fit }, Note: note}
}

func qualityRow() Row {
	return Row{Label: "画质", ShowIf: is(keySizePreset, "0", ""), Fields: []Field{
		{Key: conv.OptQuality, Type: Number, Width: 64, Placeholder: "自动", Unit: "（1~100，越大越清晰）"},
	}}
}

func stripRow() Row {
	return Row{Label: "隐私", Fields: []Field{
		{Key: conv.OptStrip, Type: Check, Label: "去掉照片里的拍摄信息（时间、地点、相机型号）"},
	}}
}

func imageTarget(p *Page, id, label, hint string, lossy bool) {
	rows := imageSizeRows()
	if id != "ico" {
		rows = append(rows, sizeRow("50 KB", "100 KB", "200 KB", "500 KB", "1 MB", "2 MB", "5 MB"))
	}
	if lossy {
		rows = append(rows, qualityRow())
	}
	if id != "ico" {
		rows = append(rows, stripRow())
	}
	add(p, &Target{ID: "img:" + id, Label: label, Hint: hint, Lane: "image", Rows: rows, Accept: conv.IsImage})
}

// ---------------------------------------------------------------- 视频

func videoRows(codecs []Choice) []Row {
	custom := is(keyRes, "custom")
	var rows []Row
	if len(codecs) > 0 {
		rows = append(rows, Row{Label: "编码", Fields: []Field{
			{Key: conv.OptVCodec, Type: Seg, Choices: codecs, Default: codecs[0].Value},
		}},
			codecNote("h264", "兼容性最好，手机、电脑、电视、剪辑软件都能放"),
			codecNote("h265", "同样清晰度体积小一半左右，很老的设备可能放不了"),
			codecNote("vp9", "网页常用，体积比 H.264 小"),
			codecNote("av1", "体积最小，但转换慢很多，老设备放不了"),
		)
	}
	rows = append(rows,
		Row{Label: "分辨率", Fields: []Field{
			{Key: keyRes, Type: Select, Default: "keep", Width: 150, Choices: choices(
				"keep", "保持原样",
				"3840x2160", "4K 3840×2160",
				"2560x1440", "2K 2560×1440",
				"1920x1080", "1080P 1920×1080",
				"1280x720", "720P 1280×720",
				"854x480", "480P 854×480",
				"640x360", "360P 640×360",
				"1080x1920", "竖屏 1080×1920",
				"720x1280", "竖屏 720×1280",
				"1080x1080", "方形 1080×1080",
				"custom", "自定义…")},
			{Key: conv.OptWidth, Type: Number, Width: 64, Placeholder: "宽", ShowIf: custom},
			{Type: Static, Label: "×", ShowIf: custom},
			{Key: conv.OptHeight, Type: Number, Width: 64, Placeholder: "高", Unit: "像素", ShowIf: custom},
			{Key: conv.OptFit, Type: Select, Default: "contain", Width: 110, ShowIf: not(is(keyRes, "keep", "")),
				Choices: choices("contain", "等比缩放", "cover", "裁剪填满", "pad", "加黑边", "stretch", "拉伸")},
		}},
		Row{Label: "画质", ShowIf: is(keySizePreset, "0", ""), Fields: []Field{
			{Key: conv.OptQuality, Type: Seg, Choices: choices("high", "高", "medium", "标准", "low", "省空间"), Default: "medium"},
		}},
		sizeRow("8 MB", "10 MB", "25 MB", "50 MB", "100 MB", "200 MB", "500 MB"),
		Row{Label: "帧率", Fields: []Field{
			{Key: conv.OptFPS, Type: Select, Default: "0", Width: 110, Choices: choices("0", "保持原样", "60", "60 帧", "30", "30 帧", "25", "25 帧", "24", "24 帧", "15", "15 帧")},
		}},
		trimRow(),
		Row{Label: "声音", Fields: []Field{{Key: conv.OptMute, Type: Check, Label: "去掉声音"}}},
		Row{Label: "加速", ShowIf: func(conv.Options) bool { return GPUAvailable() }, Fields: []Field{
			{Key: conv.OptGPU, Type: Check, Label: gpuLabel()},
		}},
	)
	return rows
}

func gpuLabel() string {
	if runtime.GOOS == "darwin" {
		return "用硬件编码加速（快很多，同样画质下文件稍大）"
	}
	return "用显卡加速（快很多，同样画质下文件稍大）"
}

func animRows(defFPS string) []Row {
	return []Row{
		{Label: "宽度", Fields: []Field{
			{Key: keyAnimW, Type: Select, Default: "480", Width: 150, Choices: choices(
				"320", "320 像素", "480", "480 像素", "640", "640 像素", "800", "800 像素", "keep", "保持原样")},
		}, Note: "动图越宽越大，发聊天用 480 就够"},
		{Label: "帧率", Fields: []Field{
			{Key: keyAnimFPS, Type: Seg, Default: defFPS, Choices: choices("8", "8", "10", "10", "12", "12", "15", "15", "20", "20", "25", "25")},
		}},
		trimRow(),
	}
}

var (
	codecMP4  = choices("h264", "H.264", "h265", "H.265", "av1", "AV1")
	codecWebM = choices("vp9", "VP9", "av1", "AV1")
)

func isVideoInput(path string) bool { return conv.IsVideo(path) || conv.Ext(path) == ".gif" }

// ---------------------------------------------------------------- 音频

func lossless(id string) bool {
	switch id {
	case "wav", "flac", "aiff":
		return true
	}
	return false
}

func audioRows(id string) []Row {
	var rows []Row
	if !lossless(id) && id != "amr" {
		rows = append(rows, Row{Label: "音质", ShowIf: is(keySizePreset, "0", ""), Fields: []Field{
			{Key: conv.OptABitrate, Type: Seg, Default: "192",
				Choices: choices("64", "64k", "96", "96k", "128", "128k", "192", "192k", "256", "256k", "320", "320k")},
		}, Note: "越大越清晰，192k 已经很好"})
	}
	if id != "amr" {
		rows = append(rows,
			Row{Label: "采样率", Fields: []Field{
				{Key: conv.OptRate, Type: Select, Default: "0", Width: 120, Choices: choices("0", "保持原样", "48000", "48000 Hz", "44100", "44100 Hz", "32000", "32000 Hz", "22050", "22050 Hz", "16000", "16000 Hz")},
			}},
			Row{Label: "声道", Fields: []Field{
				{Key: conv.OptChannels, Type: Seg, Default: "0", Choices: choices("0", "保持原样", "2", "立体声", "1", "单声道")},
			}})
	}
	if !lossless(id) && id != "amr" {
		rows = append(rows, sizeRow("1 MB", "2 MB", "5 MB", "10 MB", "20 MB"))
	}
	rows = append(rows, trimRow(),
		Row{Label: "音量", Fields: []Field{{Key: conv.OptNormalize, Type: Check, Label: "音量标准化（忽大忽小的声音变均匀）"}}})
	return rows
}

func isAudioInput(path string) bool { return conv.IsAudio(path) || conv.IsVideo(path) }

// ---------------------------------------------------------------- 文档

// docTargets 列出每类文档能转成什么（和 office 包里实际支持的一致，见测试）
var docTargets = map[conv.Family][]string{
	conv.FamWord:     {"pdf", "docx", "doc", "rtf", "odt", "txt", "html"},
	conv.FamExcel:    {"pdf", "xlsx", "xls", "csv", "ods", "html"},
	conv.FamCSV:      {"xlsx", "xls", "ods", "pdf", "html"},
	conv.FamPPT:      {"pdf", "pptx", "ppt", "odp", "mp4"},
	conv.FamText:     {"pdf", "docx", "html"},
	conv.FamMarkdown: {"html", "pdf", "docx"},
	conv.FamHTML:     {"pdf", "docx", "txt"},
}

// DocCanConvert 判断某个文档能不能转成 to（png、long 走「先转 PDF 再画成图片」）
func DocCanConvert(path, to string) bool {
	fam := conv.FamilyOf(path)
	if fam == conv.FamNone {
		return false
	}
	if to == "png" || to == "long" {
		to = "pdf"
	}
	for _, t := range docTargets[fam] {
		if t == to {
			return true
		}
	}
	return false
}

func docTarget(p *Page, id, label, hint string, rows ...Row) {
	add(p, &Target{ID: "doc:" + id, Label: label, Hint: hint, Lane: "office", Rows: rows,
		Accept: func(path string) bool { return DocCanConvert(path, id) }})
}

// ---------------------------------------------------------------- 登记

func init() {
	img := &Page{Kind: conv.Image, Title: "图片转换", Sub: "支持 JPG、PNG、HEIC（苹果照片）、WEBP、单反 RAW、PSD、SVG 等几十种格式。可以改尺寸、裁成固定像素、压到指定大小。"}
	imageTarget(img, "same", "原格式", "格式不变，只改尺寸或者压缩大小（苹果的 HEIC、RAW 这类会存成 JPG）", true)
	imageTarget(img, "jpg", "JPG", "最通用的照片格式，体积小，哪都能打开", true)
	imageTarget(img, "png", "PNG", "无损，支持透明背景，适合截图和图标", false)
	imageTarget(img, "webp", "WEBP", "网页常用，同样清晰度比 JPG 小三成左右", true)
	imageTarget(img, "avif", "AVIF", "新一代格式，比 WEBP 还小，较新的浏览器和系统才能打开", true)
	imageTarget(img, "jxl", "JXL", "JPEG XL，体积小画质好，支持它的软件还不多", true)
	imageTarget(img, "bmp", "BMP", "不压缩的位图，体积大，老软件常用", false)
	imageTarget(img, "gif", "GIF", "最多 256 色，动图会保留动画", false)
	imageTarget(img, "tiff", "TIFF", "印刷、扫描常用的无损格式", false)
	imageTarget(img, "ico", "ICO", "Windows 图标和网站图标，自动包含 16~256 像素多种尺寸", false)
	imageTarget(img, "tga", "TGA", "游戏和视频制作常用的无损格式", false)
	pdfRows := []Row{
		{Label: "纸张", Fields: []Field{{Key: conv.OptPageSize, Type: Seg, Default: "fit", Choices: choices("fit", "跟图片一样大", "a4", "A4 纸")}}},
	}
	add(img, &Target{ID: "img:pdf", Label: "PDF", Hint: "每张图片各转成一个 PDF", Lane: "image", Rows: pdfRows, Accept: conv.IsImage})
	add(img, &Target{ID: "img:pdfmerge", Label: "合成一个 PDF", Hint: "按列表里的顺序，把所有图片拼成一个 PDF（每张一页）",
		Tool: true, Batch: true, Lane: "image", Rows: pdfRows, Accept: conv.IsImage})

	vid := &Page{Kind: conv.Video, Title: "视频转换", Sub: "MP4、MKV、MOV、AVI、FLV、WMV 等格式互转，也能压缩到指定大小、改分辨率、截取片段、转成动图。"}
	vt := func(id, label, hint string, codecs []Choice) {
		add(vid, &Target{ID: "vid:" + id, Label: label, Hint: hint, Lane: "video", Rows: videoRows(codecs), Accept: isVideoInput})
	}
	vt("same", "原格式", "格式不变，只压缩、改分辨率或截取", codecMP4)
	vt("mp4", "MP4", "最通用的视频格式，手机、电脑、网站都能放", codecMP4)
	vt("mkv", "MKV", "能装多条音轨和字幕的格式，电脑播放器都支持", codecMP4)
	vt("mov", "MOV", "苹果设备和剪辑软件常用", codecMP4)
	vt("avi", "AVI", "老牌格式，兼容老播放器和车载设备", nil)
	vt("webm", "WEBM", "网页视频格式，体积小", codecWebM)
	vt("flv", "FLV", "早期网络视频格式", nil)
	vt("wmv", "WMV", "Windows 自带播放器、PPT 里插视频常用", nil)
	vt("m4v", "M4V", "苹果 iTunes 用的 MP4", codecMP4)
	vt("ts", "TS", "电视和直播录像用的格式", nil)
	vt("mpg", "MPG", "MPEG-2，DVD 和老设备兼容", nil)
	vt("3gp", "3GP", "老式手机格式", nil)
	add(vid, &Target{ID: "vid:gif", Label: "GIF 动图", Hint: "把视频（或其中一段）转成 GIF 动图，发聊天、做表情包", Lane: "video", Rows: animRows("12"), Accept: isVideoInput})
	add(vid, &Target{ID: "vid:webp", Label: "WEBP 动图", Hint: "比 GIF 清晰得多、体积更小的动图，微信和浏览器都能看", Lane: "video", Rows: animRows("15"), Accept: isVideoInput})

	aud := &Page{Kind: conv.Audio, Title: "音频转换", Sub: "MP3、FLAC、WAV、M4A 等格式互转。也能直接从视频里提取声音，或者做成苹果手机铃声。"}
	at := func(id, label, hint string) {
		add(aud, &Target{ID: "aud:" + id, Label: label, Hint: hint, Lane: "audio", Rows: audioRows(id), Accept: isAudioInput})
	}
	at("same", "原格式", "格式不变，只改音质、截取或压缩")
	at("mp3", "MP3", "最通用的音乐格式，任何设备都能放")
	at("m4a", "M4A", "苹果设备默认的格式，同样音质比 MP3 小")
	at("aac", "AAC", "不带封装的 AAC 音频")
	at("wav", "WAV", "无损不压缩，剪辑软件最爱，体积很大")
	at("flac", "FLAC", "无损压缩，音质和 WAV 一样，体积小一半")
	at("ogg", "OGG", "开源格式，游戏常用")
	at("opus", "OPUS", "低码率下音质最好，适合语音")
	at("wma", "WMA", "Windows 老格式")
	at("aiff", "AIFF", "苹果的无损格式")
	at("ac3", "AC3", "杜比数字，家庭影院用")
	at("amr", "AMR", "手机录音、语音备忘格式（8kHz 单声道）")
	at("m4r", "苹果铃声", "iPhone 铃声（.m4r），超过 40 秒会自动截取前 40 秒")

	doc := &Page{Kind: conv.Doc, Title: "文档转换", Sub: "Word、Excel、PPT、TXT、Markdown、网页互转，也能转成 PDF、图片和长图。"}
	docTarget(doc, "pdf", "PDF", "转成 PDF，排版和在 Office 里看到的一样；所有文档都能转")
	docTarget(doc, "docx", "DOCX", "Word 文档（新格式）；Word、TXT、Markdown、网页能转成它")
	docTarget(doc, "doc", "DOC", "Word 97-2003 文档，给老版本 Office 用；Word 类文档能转成它")
	docTarget(doc, "rtf", "RTF", "带格式的文本，写字板也能打开；Word 类文档能转成它")
	docTarget(doc, "odt", "ODT", "开放文档格式（LibreOffice、WPS 能打开）；Word 类文档能转成它")
	docTarget(doc, "txt", "TXT", "只保留文字；Word 文档和网页能转成它")
	docTarget(doc, "html", "HTML", "网页；Word、Excel、CSV、TXT、Markdown 能转成它")
	docTarget(doc, "xlsx", "XLSX", "Excel 表格（新格式）；Excel 类表格和 CSV 能转成它")
	docTarget(doc, "xls", "XLS", "Excel 97-2003 表格；Excel 类表格和 CSV 能转成它")
	docTarget(doc, "csv", "CSV", "逗号分隔的纯文本表格，有多个工作表时每个表一个文件；Excel 类表格能转成它")
	docTarget(doc, "ods", "ODS", "开放文档表格；Excel 类表格和 CSV 能转成它")
	docTarget(doc, "pptx", "PPTX", "PowerPoint 演示文稿（新格式）；PPT 类文件能转成它")
	docTarget(doc, "ppt", "PPT", "PowerPoint 97-2003 演示文稿；PPT 类文件能转成它")
	docTarget(doc, "odp", "ODP", "开放文档演示文稿；PPT 类文件能转成它")
	docTarget(doc, "png", "图片", "每一页转成一张 PNG 图片", dpiRow(), Row{Label: "页码", Fields: []Field{pagesField()}})
	docTarget(doc, "long", "长图", "所有页拼成一张长图，方便手机上看、发朋友圈", longRow(), Row{Label: "页码", Fields: []Field{pagesField()}})
	docTarget(doc, "mp4", "视频", "把 PPT 做成 MP4 视频，自动翻页",
		Row{Label: "每页停留", Fields: []Field{{Key: conv.OptSlideSec, Type: Number, Width: 64, Default: "5", Unit: "秒"}}, Note: "PPT 里设置过切换时间和动画的按原样播放"})

	pdf := &Page{Kind: conv.PDF, Title: "PDF 工具", Sub: "PDF 转 Word、Excel、PPT、图片、长图，还能合并、拆分、压缩、加密和解密。"}
	pt := func(id, label, hint, lane string, tool, batch bool, rows ...Row) {
		add(pdf, &Target{ID: "pdf:" + id, Label: label, Hint: hint, Tool: tool, Batch: batch, Lane: lane, Rows: rows, Accept: conv.IsPDF})
	}
	pwd := passwordRow("密码", "没有密码就不用填")
	pt("docx", "Word", "转成可以编辑的 Word 文档（用电脑上的 Word 识别版面）", "office", false, false)
	pt("xlsx", "Excel", "把 PDF 里的表格提取到 Excel，每个表格一个工作表", "office", false, false)
	pt("pptx", "PPT", "每页做成一张幻灯片（保持原样，文字不能编辑）", "pdf", false, false, dpiRow(), pwd)
	pt("jpg", "JPG 图片", "每一页转成一张 JPG 图片", "pdf", false, false, dpiRow(), Row{Label: "页码", Fields: []Field{pagesField()}}, pwd)
	pt("png", "PNG 图片", "每一页转成一张 PNG 图片（无损，适合文字多的页面）", "pdf", false, false, dpiRow(), Row{Label: "页码", Fields: []Field{pagesField()}}, pwd)
	pt("long", "长图", "所有页拼成一张长图，方便手机上看", "pdf", false, false, longRow(), Row{Label: "页码", Fields: []Field{pagesField()}}, pwd)
	pt("txt", "TXT", "提取所有文字（扫描版 PDF 没有文字可提取）", "pdf", false, false, pwd)
	pt("merge", "合并", "按列表里的顺序把所有 PDF 合成一个", "pdf", true, true,
		Row{Note: "可以在下面的列表里右键「上移」「下移」调整顺序"})
	pt("split", "拆分", "把一个 PDF 拆成多个；只要其中几页也用这个", "pdf", true, false,
		Row{Label: "拆分方式", Fields: []Field{
			{Key: conv.OptSplit, Type: Seg, Default: "each", Choices: choices("each", "每页一个文件", "every", "每几页一个", "ranges", "按页码范围")},
		}},
		Row{Label: "每", ShowIf: is(conv.OptSplit, "every"), Fields: []Field{{Key: conv.OptEvery, Type: Number, Width: 64, Default: "2", Unit: "页一个文件"}}},
		Row{Label: "页码范围", ShowIf: is(conv.OptSplit, "ranges"), Fields: []Field{{Key: conv.OptRanges, Type: Text, Width: 220, Placeholder: "比如 1-3,4-10"}},
			Note: "每段一个文件；只写一段就是把这几页提取出来"},
		pwd)
	pt("compress", "压缩", "让 PDF 变小，方便发邮件、上传", "pdf", true, false,
		Row{Label: "压缩强度", Fields: []Field{
			{Key: conv.OptLevel, Type: Seg, Default: "medium", Choices: choices("light", "轻度（无损）", "medium", "推荐", "strong", "强力")},
		}},
		Row{Label: "", ShowIf: is(conv.OptLevel, "light"), Note: "只清理冗余数据，内容完全不变，通常能小一点"},
		Row{Label: "", ShowIf: is(conv.OptLevel, "medium"), Note: "压缩里面的图片，文字保持清晰可选，扫描件和带照片的 PDF 效果明显"},
		Row{Label: "", ShowIf: is(conv.OptLevel, "strong"), Note: "每页转成图片再压缩，体积最小，但文字不能再选中、复制"},
		pwd)
	pt("encrypt", "加密", "给 PDF 加上打开密码", "pdf", true, false, Row{Label: "设置密码", Fields: []Field{
		{Key: keyNewPassword, Type: Password, Width: 180, Placeholder: "打开这个 PDF 要输入的密码"},
	}})
	pt("decrypt", "解密", "去掉 PDF 的密码和编辑限制", "pdf", true, false, passwordRow("原密码", "只有编辑限制的话不用填"))
	pt("rotate", "旋转", "把页面转个方向", "pdf", true, false,
		Row{Label: "方向", Fields: []Field{
			{Key: conv.OptAngle, Type: Seg, Default: "90", Choices: choices("90", "向右转 90°", "180", "转 180°", "270", "向左转 90°")},
		}},
		Row{Label: "页码", Fields: []Field{pagesField()}},
		pwd)

	Pages = []*Page{img, vid, aud, doc, pdf}
	bindAll()
}
