package main

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxEdge 是送給模型的圖片長邊上限。辨認品名、配件、外觀狀況 1024 就夠，
// FB 原圖常是 2048，原尺寸送過去 token 多四倍、看到的東西不會更多。
const maxEdge = 1024

// shrink 把圖片縮到長邊不超過 maxEdge 並轉成 JPEG。
func shrink(raw []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxEdge || h > maxEdge {
		if w >= h {
			h, w = h*maxEdge/w, maxEdge
		} else {
			w, h = w*maxEdge/h, maxEdge
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		src = dst
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, src, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
