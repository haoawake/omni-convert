package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/haoawake/omni-convert/internal/conv"
	"github.com/haoawake/omni-convert/internal/tools"
)

var imgMagickName = map[string]string{
	"jpg": "JPEG", "png": "PNG", "webp": "WEBP", "avif": "AVIF", "jxl": "JXL",
	"bmp": "BMP3", "gif": "GIF", "tiff": "TIFF", "ico": "ICO", "tga": "TGA",
}

func TestImageAllTargets(t *testing.T) {
	inputs := map[string]string{"jpg": fxPhoto(t), "png": fxAlphaPNG(t)}
	for _, src := range []string{"jpg", "png"} {
		in := inputs[src]
		srcStat := identify(t, in)
		for _, target := range []string{"jpg", "png", "webp", "avif", "jxl", "bmp", "gif", "tiff", "ico", "tga"} {
			t.Run(src+"→"+target, func(t *testing.T) {
				t.Parallel()
				r := runJob(t, Image(target), in, nil)
				out := r.one(t)
				if want := imgFormats[target].ext; filepath.Ext(out) != want {
					t.Errorf("扩展名是 %s，应该是 %s", filepath.Ext(out), want)
				}
				if !strings.HasPrefix(filepath.Base(out), strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))) {
					t.Errorf("输出文件名 %s 没有沿用原名", filepath.Base(out))
				}
				st := identify(t, out)
				wantFmt := imgMagickName[target]
				if wantFmt == "BMP3" {
					wantFmt = "BMP"
				}
				if target == "ico" {
					// 256×256 那一层按惯例存成 PNG，所以看文件头：00 00 01 00
					if head, _ := os.ReadFile(out); len(head) < 4 || !bytes.Equal(head[:4], []byte{0, 0, 1, 0}) {
						t.Errorf("不是 ICO 文件")
					}
				} else if st.format != wantFmt && !(target == "bmp" && strings.HasPrefix(st.format, "BMP")) {
					t.Errorf("格式是 %s，应该是 %s", st.format, wantFmt)
				}
				switch target {
				case "ico":
					if st.w != st.h || st.w > 256 || st.frames < 3 {
						t.Errorf("图标应该是正方形、最大 256、多种尺寸：%+v", st)
					}
				default:
					if st.w != srcStat.w || st.h != srcStat.h {
						t.Errorf("尺寸变了：%d×%d → %d×%d", srcStat.w, srcStat.h, st.w, st.h)
					}
				}
				if src == "png" {
					// 透明背景：JPG、BMP 铺白底；支持透明的格式保持透明
					px := pixel(t, out, 2, 2)
					switch target {
					case "jpg", "bmp":
						if !strings.Contains(px, "255,255,255") && !strings.Contains(px, "254,254,254") && !strings.Contains(px, "white") {
							t.Errorf("透明的地方应该变成白色，实际是 %s", px)
						}
					case "png", "webp", "tiff", "tga", "avif", "jxl", "gif":
						if !strings.Contains(px, ",0)") && !strings.HasSuffix(px, "a(0,0,0,0)") && !strings.Contains(px, "none") {
							t.Errorf("透明的地方应该还是透明，实际是 %s", px)
						}
					}
				}
			})
		}
	}
}

func TestImageSameFormat(t *testing.T) {
	in := fxPhoto(t)
	// 输出到原文件所在的文件夹：不能覆盖原图，要自动改名
	dir := filepath.Join(t.TempDir(), "同一个 文件夹")
	os.MkdirAll(dir, 0o755)
	src := filepath.Join(dir, "照片.jpg")
	data, _ := os.ReadFile(in)
	os.WriteFile(src, data, 0o644)
	r := runJobIn(t, Image("same"), src, conv.Options{conv.OptWidth: "600"}, dir, context.Background())
	out := r.one(t)
	if filepath.Base(out) != "照片 (1).jpg" {
		t.Errorf("输出文件名是 %s，应该是 照片 (1).jpg", filepath.Base(out))
	}
	if now, _ := os.ReadFile(src); !bytes.Equal(now, data) {
		t.Error("原文件被改动了")
	}
	if st := identify(t, out); st.w != 600 || st.h != 400 {
		t.Errorf("尺寸 %d×%d，应该是 600×400", st.w, st.h)
	}

	// HEIC、SVG 这些写不回去的格式，保持原格式时存成 JPG / PNG（有透明时）
	if out := runJob(t, Image("same"), fxSVG(t), nil).one(t); filepath.Ext(out) != ".png" {
		t.Errorf("SVG 保持原格式应该存成 PNG，实际是 %s", out)
	}
	if out := runJob(t, Image("same"), fxGIF(t), nil).one(t); identify(t, out).frames != 4 {
		t.Error("动图保持原格式应该还是 4 帧")
	}
}

func TestImageResizeModes(t *testing.T) {
	in := fxPhoto(t) // 1200×800
	cases := []struct {
		name string
		opt  conv.Options
		w, h int
	}{
		{"证件照 413×579", conv.Options{"resize": "box", "w": "413", "h": "579", "fit": "cover"}, 413, 579},
		{"方图 1080×1080", conv.Options{"resize": "box", "w": "1080", "h": "1080", "fit": "cover"}, 1080, 1080},
		{"等比放进 500×500", conv.Options{"resize": "box", "w": "500", "h": "500", "fit": "contain"}, 500, 333},
		{"补边到 500×500", conv.Options{"resize": "box", "w": "500", "h": "500", "fit": "pad", "bg": "#ff0000"}, 500, 500},
		{"拉伸 300×300", conv.Options{"resize": "box", "w": "300", "h": "300", "fit": "stretch"}, 300, 300},
		{"只填宽", conv.Options{"resize": "box", "w": "600"}, 600, 400},
		{"只填高", conv.Options{"resize": "box", "h": "200"}, 300, 200},
		{"百分比", conv.Options{"resize": "percent", "percent": "50"}, 600, 400},
		{"限制长边", conv.Options{"resize": "long", "long": "900"}, 900, 600},
		{"长边本来就小", conv.Options{"resize": "long", "long": "3000"}, 1200, 800},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out := runJob(t, Image("jpg"), in, c.opt).one(t)
			st := identify(t, out)
			if st.w != c.w || st.h != c.h {
				t.Fatalf("尺寸 %d×%d，应该是 %d×%d", st.w, st.h, c.w, c.h)
			}
			if c.opt["fit"] == "pad" {
				if px := pixel(t, out, 250, 2); !looksRed(px) { // JPEG 有损，接近纯红就行
					t.Errorf("补边颜色应该是红色，实际是 %s", px)
				}
			}
		})
	}
	// 竖拍照片裁成证件照
	t.Run("竖拍照片 cover", func(t *testing.T) {
		out := runJob(t, Image("png"), fxRotated(t), conv.Options{"w": "120", "h": "120", "fit": "cover"}).one(t)
		if st := identify(t, out); st.w != 120 || st.h != 120 {
			t.Errorf("尺寸 %d×%d，应该是 120×120", st.w, st.h)
		}
	})
}

func looksRed(px string) bool {
	px = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(px, "srgba("), "srgb("), ")")
	parts := strings.Split(px, ",")
	if len(parts) < 3 {
		return false
	}
	var v [3]float64
	for i := range 3 {
		v[i] = parseFloat(strings.TrimSuffix(parts[i], "%"))
	}
	return v[0] > 200 && v[1] < 60 && v[2] < 60
}

func TestImageAutoOrient(t *testing.T) {
	out := runJob(t, Image("png"), fxRotated(t), nil).one(t)
	st := identify(t, out)
	if st.w != 200 || st.h != 300 {
		t.Fatalf("应该按 EXIF 转正成 200×300，实际是 %d×%d", st.w, st.h)
	}
	if px := pixel(t, out, 100, 40); !looksRed(px) {
		t.Errorf("转正后上半部分应该是红色，实际是 %s", px)
	}
	// 去掉拍摄信息
	out = runJob(t, Image("jpg"), fxRotated(t), conv.Options{conv.OptStrip: "1"}).one(t)
	res, _ := tools.Run(context.Background(), tools.Magick(), []string{"identify", "-quiet", "-format", "%[EXIF:*]", out}, nil)
	if strings.TrimSpace(string(res)) != "" {
		t.Errorf("strip 之后还有 EXIF：%s", res)
	}
}

func TestImageCMYK(t *testing.T) {
	out := runJob(t, Image("jpg"), fxCMYK(t), nil).one(t)
	if st := identify(t, out); st.colorspace != "sRGB" {
		t.Errorf("CMYK 应该转成 sRGB，实际是 %s", st.colorspace)
	}
}

func TestImageAnimated(t *testing.T) {
	gif := fxGIF(t)
	t.Run("gif→webp 保留动画", func(t *testing.T) {
		if st := identify(t, runJob(t, Image("webp"), gif, nil).one(t)); st.frames != 4 {
			t.Errorf("应该有 4 帧，实际 %d 帧", st.frames)
		}
	})
	t.Run("gif 缩小保留动画", func(t *testing.T) {
		st := identify(t, runJob(t, Image("gif"), gif, conv.Options{"w": "80"}).one(t))
		if st.frames != 4 || st.w != 80 || st.h != 60 {
			t.Errorf("应该是 4 帧 80×60，实际 %d 帧 %d×%d", st.frames, st.w, st.h)
		}
	})
	t.Run("gif 裁成方形", func(t *testing.T) {
		st := identify(t, runJob(t, Image("webp"), gif, conv.Options{"w": "64", "h": "64", "fit": "cover"}).one(t))
		if st.frames != 4 || st.w != 64 || st.h != 64 {
			t.Errorf("应该是 4 帧 64×64，实际 %d 帧 %d×%d", st.frames, st.w, st.h)
		}
	})
	t.Run("gif→jpg 取第一帧", func(t *testing.T) {
		st := identify(t, runJob(t, Image("jpg"), gif, nil).one(t))
		if st.frames != 1 || st.w != 160 {
			t.Errorf("应该是 1 帧 160 宽，实际 %d 帧 %d 宽", st.frames, st.w)
		}
	})
}

func TestImageICO(t *testing.T) {
	// 多尺寸图标转 PNG：取最大的那个
	if st := identify(t, runJob(t, Image("png"), fxICO(t), nil).one(t)); st.w != 256 || st.h != 256 {
		t.Errorf("应该取最大的 256×256，实际 %d×%d", st.w, st.h)
	}
	// 小图转图标：不放大到 256，只放不超过原图的尺寸
	out := runJob(t, Image("ico"), fxGIF(t), nil).one(t) // 160×120 → 补成 160×160
	st := identify(t, out)
	if st.w != 160 || st.h != 160 || st.frames != 6 { // 160、128、64、48、32、16
		t.Errorf("图标应该是 160×160 起 6 种尺寸，实际 %d×%d %d 种", st.w, st.h, st.frames)
	}
}

func TestImageHEIC(t *testing.T) {
	in := fxHEIC(t)
	r := runJob(t, Image("same"), in, nil)
	out := r.one(t)
	if filepath.Ext(out) != ".jpg" {
		t.Errorf("HEIC 保持原格式应该存成 JPG，实际是 %s", out)
	}
	if st := identify(t, out); st.w != 640 || st.h != 480 {
		t.Errorf("尺寸 %d×%d，应该是 640×480", st.w, st.h)
	}
	for _, target := range []string{"png", "webp"} {
		if st := identify(t, runJob(t, Image(target), in, conv.Options{"resize": "long", "long": "320"}).one(t)); st.w != 320 || st.h != 240 {
			t.Errorf("HEIC→%s 尺寸 %d×%d，应该是 320×240", target, st.w, st.h)
		}
	}
}

func TestImageSVG(t *testing.T) {
	st := identify(t, runJob(t, Image("png"), fxSVG(t), nil).one(t))
	if st.w < 1024 || st.w != st.h {
		t.Errorf("小 SVG 应该渲染得足够清楚（≥1024），实际 %d×%d", st.w, st.h)
	}
}

func TestImageSpecialName(t *testing.T) {
	data, _ := os.ReadFile(fxAlphaPNG(t))
	in := filepath.Join(t.TempDir(), "奇怪[1] 名字 100%.png")
	os.WriteFile(in, data, 0o644)
	out := runJob(t, Image("jpg"), in, nil).one(t)
	if filepath.Base(out) != "奇怪[1] 名字 100%.jpg" {
		t.Errorf("输出文件名 %q 不对", filepath.Base(out))
	}
	if st := identify(t, out); st.w != 400 {
		t.Errorf("宽度 %d，应该是 400", st.w)
	}
}

func TestImageCorrupt(t *testing.T) {
	in := filepath.Join(t.TempDir(), "坏掉的 图.jpg")
	os.WriteFile(in, []byte("这不是图片"), 0o644)
	ue := runJob(t, Image("png"), in, nil).userErr(t)
	if !strings.Contains(ue.Msg, "读不出") {
		t.Errorf("错误信息不够清楚：%v", ue)
	}
	if ue := runJob(t, Image("heic"), fxPhoto(t), nil).userErr(t); !strings.Contains(ue.Msg, "不支持") {
		t.Errorf("错误信息不对：%v", ue)
	}
}

func TestImageTargetSize(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过比较慢的压缩测试")
	}
	big := fxBigPhoto(t) // 3000×2000，细节很多
	cases := []struct {
		name, format string
		opt          conv.Options
		size         int64
	}{
		{"jpg 50KB", "jpg", nil, 50 << 10},
		{"jpg 300KB 只降质量", "jpg", nil, 300 << 10},
		{"webp 40KB", "webp", nil, 40 << 10},
		{"avif 60KB", "avif", nil, 60 << 10},
		{"jxl 80KB", "jxl", nil, 80 << 10},
		{"png 200KB", "png", nil, 200 << 10},
		{"bmp 500KB", "bmp", nil, 500 << 10},
		{"证件照 413×579 30KB", "jpg", conv.Options{"w": "413", "h": "579", "fit": "cover"}, 30 << 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			opt := c.opt.Clone()
			opt[conv.OptTargetSize] = strconv.FormatInt(c.size, 10)
			r := runJob(t, Image(c.format), big, opt)
			out := r.one(t)
			sz := fileSize(out)
			st := identify(t, out)
			t.Logf("%s：%s，%d×%d，用时 %v", c.name, humanSize(sz), st.w, st.h, r.took.Round(1e7))
			if sz > c.size {
				t.Fatalf("结果 %s 超过了目标 %s", humanSize(sz), humanSize(c.size))
			}
			if c.opt["fit"] == "cover" && (st.w != 413 || st.h != 579) {
				t.Errorf("固定尺寸不该被改：%d×%d", st.w, st.h)
			}
			if c.name == "jpg 300KB 只降质量" && (st.w != 3000 || st.h != 2000) {
				t.Errorf("降质量就够了，不该缩小尺寸：%d×%d", st.w, st.h)
			}
			if !strings.Contains(strings.Join(r.notes, "|"), "正在压缩到") {
				t.Errorf("进度说明里应该有「正在压缩到」：%v", r.notes)
			}
		})
	}
	t.Run("固定尺寸压不到", func(t *testing.T) {
		t.Parallel()
		ue := runJob(t, Image("png"), big, conv.Options{"w": "1000", "h": "1000", "fit": "cover", conv.OptTargetSize: "2048"}).userErr(t)
		if !strings.Contains(ue.Msg, "压不到 2 KB") {
			t.Errorf("错误信息不对：%v", ue)
		}
		t.Log(ue)
	})
}

func TestPrepareForPDF(t *testing.T) {
	needTools(t)
	ctx := context.Background()
	tmp := filepath.Join(t.TempDir(), "临时 文件")
	os.MkdirAll(tmp, 0o755)

	// 不用改的 JPEG、PNG 原样返回
	for _, in := range []string{fxPhoto(t), fxAlphaPNG(t)} {
		got, err := PrepareForPDF(ctx, in, nil, tmp)
		if err != nil || got != in {
			t.Errorf("%s 应该原样返回，实际 %q，%v", filepath.Base(in), got, err)
		}
	}
	check := func(in string, opt conv.Options, ext string, w, h int) {
		t.Helper()
		got, err := PrepareForPDF(ctx, in, opt, tmp)
		if err != nil {
			t.Fatalf("%s：%v", filepath.Base(in), err)
		}
		if got == in || filepath.Dir(got) != tmp || filepath.Ext(got) != ext {
			t.Errorf("%s 应该生成 %s 到临时文件夹，实际 %s", filepath.Base(in), ext, got)
		}
		if st := identify(t, got); st.w != w || st.h != h || st.colorspace != "sRGB" && st.colorspace != "Gray" {
			t.Errorf("%s：%d×%d %s，应该是 %d×%d sRGB", filepath.Base(in), st.w, st.h, st.colorspace, w, h)
		}
	}
	check(fxRotated(t), nil, ".jpg", 200, 300)                                         // 转正
	check(fxCMYK(t), nil, ".jpg", 640, 480)                                            // CMYK 转 sRGB
	check(fxPhoto(t), conv.Options{"resize": "long", "long": "600"}, ".jpg", 600, 400) // 缩放
	check(fxGIF(t), nil, ".png", 160, 120)                                             // 动图取第一帧，无损来源存 PNG
	check(fxHEIC(t), nil, ".jpg", 640, 480)                                            // 照片存 JPEG
	check(fxSVG(t), nil, ".png", 2048, 2048)                                           // 矢量图渲染清楚
	// 原文件没被动
	if _, err := os.Stat(fxRotated(t)); err != nil {
		t.Error(err)
	}
}

func TestImageInputs(t *testing.T) {
	// 各种格式当输入：先用 magick 做出来，再转成 PNG
	photo := fxPhoto(t)
	cases := []struct {
		name string
		make func(string) error
		w, h int
	}{
		{"图片.webp", magickCmd(photo, "-quality", "80"), 1200, 800},
		{"图片.avif", magickCmd(photo, "-resize", "50%"), 600, 400},
		{"图片.jxl", magickCmd(photo, "-resize", "50%"), 600, 400},
		{"图片.tga", magickCmd(photo), 1200, 800},
		{"图片.bmp", magickCmd(photo), 1200, 800},
		{"图片.tif", magickCmd(photo, "-compress", "LZW"), 1200, 800},
		{"图层.psd", magickCmd("-size", "300x200", "xc:orange", "(", "-size", "100x100", "xc:red", ")", "(", "-size", "80x80", "xc:blue", ")"), 300, 200},
		{"多页.tif", magickCmd("-size", "300x200", "xc:red", "xc:green", "xc:blue"), 300, 200},
		{"图片.jp2", magickCmd(photo, "-resize", "25%"), 300, 200},
		{"图片.qoi", magickCmd(photo, "-resize", "25%"), 300, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := fixture(t, "输入 "+c.name, c.make)
			out := runJob(t, Image("png"), in, nil).one(t)
			st := identify(t, out)
			if st.w != c.w || st.h != c.h || st.frames != 1 {
				t.Errorf("结果 %d×%d %d 帧，应该是 %d×%d 1 帧", st.w, st.h, st.frames, c.w, c.h)
			}
		})
	}
	t.Run("多页 TIFF 转 TIFF 保留全部页", func(t *testing.T) {
		in := fixture(t, "输入 多页.tif", magickCmd("-size", "300x200", "xc:red", "xc:green", "xc:blue"))
		if st := identify(t, runJob(t, Image("tiff"), in, nil).one(t)); st.frames != 3 {
			t.Errorf("应该还是 3 页，实际 %d 页", st.frames)
		}
	})
}
