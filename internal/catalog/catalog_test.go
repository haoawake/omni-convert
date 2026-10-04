package catalog

import (
	"testing"

	"github.com/haoawake/omni-convert/internal/conv"
)

func TestEveryTargetIsBound(t *testing.T) {
	if len(Pages) != int(conv.NumKinds) {
		t.Fatalf("应该有 %d 页，实际 %d", conv.NumKinds, len(Pages))
	}
	for _, p := range Pages {
		if len(p.Targets) == 0 {
			t.Errorf("%s 页没有按钮", p.Title)
		}
		for _, tg := range p.Targets {
			if tg.Run == nil {
				t.Errorf("%s 没有接上转换器", tg.ID)
			}
			if tg.Accept == nil || tg.Lane == "" || tg.Label == "" || tg.Hint == "" {
				t.Errorf("%s 缺少信息", tg.ID)
			}
			if ByID(tg.ID) != tg {
				t.Errorf("%s 重复登记", tg.ID)
			}
		}
	}
}

func TestPrepare(t *testing.T) {
	tg := ByID("img:jpg")
	o := tg.Prepare(conv.Options{keySizePreset: "custom", keySizeNum: "300", keySizeUnit: "KB", conv.OptResize: "box", conv.OptWidth: "413", conv.OptHeight: "579"})
	if o[conv.OptTargetSize] != "307200" {
		t.Errorf("大小换算错了：%v", o)
	}
	if o[conv.OptFit] != "cover" || o[conv.OptWidth] != "413" {
		t.Errorf("尺寸选项不对：%v", o)
	}
	if _, ok := o[keySizePreset]; ok {
		t.Error("界面专用的键不该传给转换器")
	}
	// 质量在指定大小时隐藏，不应该传下去
	if _, ok := o[conv.OptQuality]; ok {
		t.Error("指定大小时不该带质量")
	}
	// 尺寸「保持原样」时，宽高不该传下去
	o = tg.Prepare(conv.Options{conv.OptWidth: "100"})
	if _, ok := o[conv.OptWidth]; ok || o[conv.OptResize] != "none" {
		t.Errorf("保持原样时不该带宽高：%v", o)
	}

	v := ByID("vid:mp4")
	o = v.Prepare(conv.Options{keyRes: "1280x720"})
	if o[conv.OptResize] != "box" || o[conv.OptWidth] != "1280" || o[conv.OptHeight] != "720" || o[conv.OptFit] != "contain" {
		t.Errorf("分辨率预设换算错了：%v", o)
	}
	o = v.Prepare(conv.Options{})
	if o[conv.OptResize] != "none" || o[conv.OptQuality] != "medium" || o[conv.OptVCodec] != "h264" {
		t.Errorf("默认值不对：%v", o)
	}
	o = v.Prepare(conv.Options{keySizePreset: "10485760"})
	if o[conv.OptTargetSize] != "10485760" {
		t.Errorf("视频大小预设不对：%v", o)
	}
	g := ByID("vid:gif")
	o = g.Prepare(conv.Options{})
	if o[conv.OptWidth] != "480" || o[conv.OptHeight] != "0" || o[conv.OptFPS] != "12" {
		t.Errorf("GIF 默认值不对：%v", o)
	}
	// 同一页的 MP4 选了 1080P、30 帧，切到 GIF 不能带过去；反过来也一样
	ui := conv.Options{keyRes: "1920x1080", conv.OptFPS: "30"}
	v.Defaults(ui)
	g.Defaults(ui)
	o = g.Prepare(ui)
	if o[conv.OptWidth] != "480" || o[conv.OptFPS] != "12" {
		t.Errorf("MP4 的设置漏到了 GIF：%v", o)
	}
	ui[keyAnimW], ui[keyAnimFPS] = "640", "10"
	v.Defaults(ui)
	o = v.Prepare(ui)
	if o[conv.OptWidth] != "1920" || o[conv.OptFPS] != "30" {
		t.Errorf("GIF 的设置漏到了 MP4：%v", o)
	}
	// 旧设置里不认识的值改回默认
	bad := conv.Options{conv.OptFPS: "7", keyRes: "999x999"}
	v.Defaults(bad)
	if bad[conv.OptFPS] != "0" || bad[keyRes] != "keep" {
		t.Errorf("无效的值没改回默认：%v", bad)
	}
}

func TestPresetApply(t *testing.T) {
	o := conv.Options{}
	for _, r := range ByID("img:png").Rows {
		for _, f := range r.Fields {
			if f.Key == keyPreset {
				f.Apply(o, "295x413")
			}
		}
	}
	if o[conv.OptResize] != "box" || o[conv.OptWidth] != "295" || o[conv.OptHeight] != "413" || o[conv.OptFit] != "cover" {
		t.Errorf("一寸照预设没生效：%v", o)
	}
}

func TestDocMatrix(t *testing.T) {
	ok := map[[2]string]bool{
		{"a.docx", "pdf"}: true, {"a.docx", "xlsx"}: false, {"a.xlsx", "csv"}: true, {"a.pptx", "mp4"}: true,
		{"a.md", "html"}: true, {"a.csv", "xlsx"}: true, {"a.pptx", "long"}: true, {"a.txt", "xlsx"}: false,
	}
	for k, want := range ok {
		if got := DocCanConvert(k[0], k[1]); got != want {
			t.Errorf("DocCanConvert(%s, %s) = %v", k[0], k[1], got)
		}
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		id  string
		ui  conv.Options
		bad bool
	}{
		{"img:jpg", conv.Options{}, false},
		{"img:jpg", conv.Options{keySizePreset: "custom"}, true},
		{"img:jpg", conv.Options{keySizePreset: "custom", keySizeNum: "300"}, false},
		{"img:jpg", conv.Options{conv.OptResize: "box"}, true},
		{"img:jpg", conv.Options{conv.OptResize: "box", conv.OptWidth: "300", conv.OptFit: "contain"}, false},
		{"img:jpg", conv.Options{conv.OptResize: "box", conv.OptWidth: "300", conv.OptFit: "cover"}, true},
		{"img:jpg", conv.Options{conv.OptQuality: "300"}, true},
		{"vid:mp4", conv.Options{conv.OptStart: "1:00", conv.OptEnd: "0:30"}, true},
		{"vid:mp4", conv.Options{conv.OptStart: "abc"}, true},
		{"vid:mp4", conv.Options{conv.OptStart: "0:10", conv.OptEnd: "1:30"}, false},
		{"pdf:encrypt", conv.Options{}, true},
		{"pdf:encrypt", conv.Options{conv.OptPassword: "123"}, true}, // 解密时填的旧密码不算
		{"pdf:encrypt", conv.Options{keyNewPassword: "123"}, false},
		{"pdf:split", conv.Options{conv.OptSplit: "ranges"}, true},
		{"pdf:split", conv.Options{conv.OptSplit: "ranges", conv.OptRanges: "1-3,5"}, false},
		{"pdf:jpg", conv.Options{conv.OptPages: "x-y"}, true},
	}
	for _, c := range cases {
		tg := ByID(c.id)
		msg := tg.Check(tg.Prepare(c.ui))
		if (msg != "") != c.bad {
			t.Errorf("%s %v：Check = %q", c.id, c.ui, msg)
		}
	}
}
