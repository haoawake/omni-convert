package pdf

// 图片缩小（面积平均，缩小时效果和速度都不错）以及几种像素格式之间的转换

import (
	"image"
	"image/color"
	"math"
)

// contrib 是缩小时一个目标像素由哪些源像素组成：从 start 开始，权重之和是 1<<16
type contrib struct {
	start int
	w     []uint32
}

func areaWeights(src, dst int) []contrib {
	out := make([]contrib, dst)
	scale := float64(src) / float64(dst)
	for i := range out {
		a := float64(i) * scale
		b := a + scale
		start := int(a)
		end := min(int(math.Ceil(b-1e-9)), src)
		if end <= start {
			end = start + 1
		}
		ws := make([]uint32, 0, end-start)
		var sum uint32
		for s := start; s < end; s++ {
			lo := math.Max(a, float64(s))
			hi := math.Min(b, float64(s+1))
			v := uint32(math.Round(math.Max(hi-lo, 0) / scale * 65536))
			ws = append(ws, v)
			sum += v
		}
		// 把舍入误差补到最大的那个权重上，保证总和正好是 65536
		big := 0
		for k := range ws {
			if ws[k] > ws[big] {
				big = k
			}
		}
		ws[big] += 65536 - sum
		out[i] = contrib{start, ws}
	}
	return out
}

// downscale 把 ch 个通道交错存放的 w×h 像素缩小到 nw×nh（nw≤w、nh≤h）
func downscale(src []byte, w, h, ch, nw, nh int) []byte {
	if nw == w && nh == h {
		return src
	}
	cx := areaWeights(w, nw)
	cy := areaWeights(h, nh)
	// 先横向：结果放大 256 倍存在 uint16 里
	tmp := make([]uint16, nw*h*ch)
	acc := make([]uint64, ch)
	for y := 0; y < h; y++ {
		row := src[y*w*ch : (y+1)*w*ch]
		trow := tmp[y*nw*ch : (y+1)*nw*ch]
		for x, c := range cx {
			for k := range acc {
				acc[k] = 0
			}
			for i, wt := range c.w {
				p := row[(c.start+i)*ch:]
				for k := 0; k < ch; k++ {
					acc[k] += uint64(p[k]) * uint64(wt)
				}
			}
			for k := 0; k < ch; k++ {
				trow[x*ch+k] = uint16((acc[k] + 128) >> 8)
			}
		}
	}
	// 再纵向
	dst := make([]byte, nw*nh*ch)
	n := nw * ch
	col := make([]uint64, n)
	for y, c := range cy {
		for k := range col {
			col[k] = 0
		}
		for i, wt := range c.w {
			trow := tmp[(c.start+i)*n : (c.start+i+1)*n]
			for k, v := range trow {
				col[k] += uint64(v) * uint64(wt)
			}
		}
		drow := dst[y*n : (y+1)*n]
		for k, v := range col {
			drow[k] = uint8(min((v+(1<<23))>>24, 255))
		}
	}
	return dst
}

// packPixels 把图片转成紧凑的 RGB（ch=3）或灰度（ch=1）字节
func packPixels(m image.Image, ch int) []byte {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, w*h*ch)
	i := 0
	switch t := m.(type) {
	case *image.YCbCr:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				yi := t.YOffset(x, y)
				if ch == 1 {
					out[i] = t.Y[yi]
					i++
					continue
				}
				ci := t.COffset(x, y)
				r, g, bb := color.YCbCrToRGB(t.Y[yi], t.Cb[ci], t.Cr[ci])
				out[i], out[i+1], out[i+2] = r, g, bb
				i += 3
			}
		}
	case *image.Gray:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := t.Pix[t.PixOffset(b.Min.X, y):]
			for x := 0; x < w; x++ {
				if ch == 1 {
					out[i] = row[x]
					i++
				} else {
					out[i], out[i+1], out[i+2] = row[x], row[x], row[x]
					i += 3
				}
			}
		}
	case *image.RGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			row := t.Pix[t.PixOffset(b.Min.X, y):]
			for x := 0; x < w; x++ {
				p := row[x*4:]
				if ch == 1 {
					out[i] = uint8((19595*uint32(p[0]) + 38470*uint32(p[1]) + 7471*uint32(p[2]) + 1<<15) >> 16)
					i++
				} else {
					out[i], out[i+1], out[i+2] = p[0], p[1], p[2]
					i += 3
				}
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bb, _ := m.At(x, y).RGBA()
				if ch == 1 {
					out[i] = uint8((19595*r + 38470*g + 7471*bb + 1<<15) >> 24)
					i++
				} else {
					out[i], out[i+1], out[i+2] = uint8(r>>8), uint8(g>>8), uint8(bb>>8)
					i += 3
				}
			}
		}
	}
	return out
}

// unpack 把紧凑的像素变回能交给 jpeg 编码器的图片
func unpack(pix []byte, w, h, ch int) image.Image {
	if ch == 1 {
		return &image.Gray{Pix: pix, Stride: w, Rect: image.Rect(0, 0, w, h)}
	}
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, j := 0, 0; i+2 < len(pix); i, j = i+3, j+4 {
		m.Pix[j], m.Pix[j+1], m.Pix[j+2], m.Pix[j+3] = pix[i], pix[i+1], pix[i+2], 0xff
	}
	return m
}

// grayIfNeutral 页面上几乎没有颜色时转成灰度图（JPEG 更小），否则原样返回
func grayIfNeutral(m *image.RGBA) image.Image {
	p := m.Pix
	for i := 0; i+3 < len(p); i += 4 {
		r, g, b := int(p[i]), int(p[i+1]), int(p[i+2])
		if abs(r-g) > 6 || abs(g-b) > 6 || abs(r-b) > 6 {
			return m
		}
	}
	w, h := m.Rect.Dx(), m.Rect.Dy()
	return unpack(packPixels(m, 1), w, h, 1)
}
