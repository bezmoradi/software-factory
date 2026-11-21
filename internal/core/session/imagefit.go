package session

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"sync"
)

const (
	MaxImageUpload = 32 << 20
	MaxImageSide   = 1568
	maxImagePixels = 25_000_000
	ImageRefused   = "That image is too large to send, or not one the page can read."
)

var fitting sync.Mutex

func decodable(kind string) bool {
	return kind == "image/png" || kind == "image/jpeg"
}

func fitImage(kind string, data []byte) (string, []byte, bool) {
	if !decodable(kind) {
		return kind, data, len(data) <= MaxImage
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return kind, data, len(data) <= MaxImage
	}
	side := max(cfg.Width, cfg.Height)
	if side <= MaxImageSide && len(data) <= MaxImage {
		return kind, data, true
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return kind, data, false
	}
	fitting.Lock()
	defer fitting.Unlock()
	var src image.Image
	if kind == "image/png" {
		src, err = png.Decode(bytes.NewReader(data))
	} else {
		src, err = jpeg.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return kind, data, false
	}
	scale := min(1, float64(MaxImageSide)/float64(side))
	small := shrunk(src, max(1, int(float64(cfg.Width)*scale+0.5)), max(1, int(float64(cfg.Height)*scale+0.5)))
	out, outKind := encodeImage(small, kind == "image/jpeg", 90)
	if len(out) > MaxImage {
		out, outKind = encodeImage(small, true, 85)
	}
	if out == nil || len(out) > MaxImage {
		return kind, data, false
	}
	return outKind, out, true
}

func encodeImage(img image.Image, asJPEG bool, quality int) ([]byte, string) {
	var buf bytes.Buffer
	if asJPEG {
		if jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}) != nil {
			return nil, ""
		}
		return buf.Bytes(), "image/jpeg"
	}
	if png.Encode(&buf, img) != nil {
		return nil, ""
	}
	return buf.Bytes(), "image/png"
}

func shrunk(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	full := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(full, full.Bounds(), src, b.Min, draw.Src)
	sw, sh := b.Dx(), b.Dy()
	if sw == w && sh == h {
		return full
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	sums := make([]uint64, w*4)
	counts := make([]uint64, w)
	for dy := range h {
		y0, y1 := dy*sh/h, max((dy+1)*sh/h, dy*sh/h+1)
		clear(sums)
		clear(counts)
		for y := y0; y < y1; y++ {
			row := full.Pix[y*full.Stride:]
			for dx := range w {
				x0, x1 := dx*sw/w, max((dx+1)*sw/w, dx*sw/w+1)
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					sums[dx*4] += uint64(p[0])
					sums[dx*4+1] += uint64(p[1])
					sums[dx*4+2] += uint64(p[2])
					sums[dx*4+3] += uint64(p[3])
				}
				counts[dx] += uint64(x1 - x0)
			}
		}
		dst := out.Pix[dy*out.Stride:]
		for dx := range w {
			n := counts[dx]
			for c := range 4 {
				dst[dx*4+c] = uint8((sums[dx*4+c] + n/2) / n)
			}
		}
	}
	return out
}
