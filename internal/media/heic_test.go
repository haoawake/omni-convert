package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// ImageMagick 和 ffmpeg 都写不了 HEIC，测试只好自己做一个：用 x265 编一帧画面，再按 HEIF 格式包起来
// （和 iPhone 拍的照片是同一种格式，只是没有分块）。

func fxHEIC(t testing.TB) string {
	return fixture(t, "苹果 照片.heic", func(p string) error {
		raw := p + ".hevc"
		defer os.Remove(raw)
		if err := ffmpegCmd("-f", "lavfi", "-i", "testsrc2=size=640x480", "-frames:v", "1",
			"-c:v", "libx265", "-pix_fmt", "yuv420p", "-x265-params", "log-level=error", "-f", "hevc")(raw); err != nil {
			return err
		}
		data, err := os.ReadFile(raw)
		if err != nil {
			return err
		}
		heic, err := buildHEIC(data, 640, 480)
		if err != nil {
			return err
		}
		return os.WriteFile(p, heic, 0o644)
	})
}

// splitAnnexB 按 00 00 01 起始码切出 NAL 单元
func splitAnnexB(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				out = append(out, bytes.TrimRight(b[start:i], "\x00"))
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	out = append(out, typ...)
	return append(out, body...)
}

func fullBox(typ string, version byte, parts ...[]byte) []byte {
	return box(typ, append([][]byte{{version, 0, 0, 0}}, parts...)...)
}

func u16(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) }
func u32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }

func buildHEIC(stream []byte, w, h int) ([]byte, error) {
	var vps, sps, pps, vcl [][]byte
	for _, n := range splitAnnexB(stream) {
		if len(n) < 2 {
			continue
		}
		switch typ := n[0] >> 1 & 0x3f; {
		case typ == 32:
			vps = append(vps, n)
		case typ == 33:
			sps = append(sps, n)
		case typ == 34:
			pps = append(pps, n)
		case typ < 32:
			vcl = append(vcl, n)
		}
	}
	if len(vps) == 0 || len(sps) == 0 || len(pps) == 0 || len(vcl) == 0 {
		return nil, errors.New("HEVC 码流里缺参数集或者画面")
	}
	// 从 SPS 里抄 profile_tier_level（先去掉防竞争字节）
	var rbsp []byte
	s := sps[0][2:]
	for i := 0; i < len(s); i++ {
		if i >= 2 && s[i] == 3 && s[i-1] == 0 && s[i-2] == 0 {
			continue
		}
		rbsp = append(rbsp, s[i])
	}
	if len(rbsp) < 13 {
		return nil, errors.New("SPS 太短")
	}
	hvcc := []byte{1}
	hvcc = append(hvcc, rbsp[1:13]...)                            // profile、兼容标志、约束标志、level
	hvcc = append(hvcc, 0xF0, 0x00, 0xFC, 0xFD, 0xF8, 0xF8, 0, 0) // 4:2:0、8 位
	hvcc = append(hvcc, 0x0F, 3)                                  // NAL 长度 4 字节；3 组参数集
	for _, arr := range []struct {
		typ  byte
		nals [][]byte
	}{{32, vps}, {33, sps}, {34, pps}} {
		hvcc = append(hvcc, 0x80|arr.typ)
		hvcc = append(hvcc, u16(len(arr.nals))...)
		for _, n := range arr.nals {
			hvcc = append(hvcc, u16(len(n))...)
			hvcc = append(hvcc, n...)
		}
	}
	var payload []byte
	for _, n := range vcl {
		payload = append(payload, u32(len(n))...)
		payload = append(payload, n...)
	}

	ftyp := box("ftyp", []byte("heic"), u32(0), []byte("mif1heic"))
	meta := func(offset int) []byte {
		return fullBox("meta", 0,
			fullBox("hdlr", 0, u32(0), []byte("pict"), make([]byte, 12), []byte{0}),
			fullBox("pitm", 0, u16(1)),
			fullBox("iloc", 0, []byte{0x44, 0x00}, u16(1), u16(1), u16(0), u16(1), u32(offset), u32(len(payload))),
			fullBox("iinf", 0, u16(1), fullBox("infe", 2, u16(1), u16(0), []byte("hvc1"), []byte{0})),
			box("iprp",
				box("ipco", box("hvcC", hvcc), fullBox("ispe", 0, u32(w), u32(h))),
				fullBox("ipma", 0, u32(1), u16(1), []byte{2, 0x81, 0x02})),
		)
	}
	offset := len(ftyp) + len(meta(0)) + 8
	return bytes.Join([][]byte{ftyp, meta(offset), box("mdat", payload)}, nil), nil
}
