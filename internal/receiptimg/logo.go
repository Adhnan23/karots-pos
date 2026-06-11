package receiptimg

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"  // register decoders
	_ "image/jpeg" //
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
)

// LoadImage fetches a logo from a data: URI, an http(s) URL, or a /static/ path
// served from the embedded asset FS, and decodes it (PNG/JPEG/GIF).
func LoadImage(ctx context.Context, src string, staticFS fs.FS) (image.Image, error) {
	src = strings.TrimSpace(src)
	var data []byte
	switch {
	case strings.HasPrefix(src, "data:"):
		i := strings.IndexByte(src, ',')
		if i < 0 {
			return nil, fmt.Errorf("invalid data uri")
		}
		if strings.Contains(src[:i], "base64") {
			b, err := base64.StdEncoding.DecodeString(src[i+1:])
			if err != nil {
				return nil, err
			}
			data = b
		} else {
			data = []byte(src[i+1:])
		}
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, err
		}
		resp, err := (&http.Client{Timeout: 6 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("logo http %d", resp.StatusCode)
		}
		if data, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20)); err != nil {
			return nil, err
		}
	case strings.HasPrefix(src, "/static/"):
		if staticFS == nil {
			return nil, fmt.Errorf("no static fs")
		}
		b, err := fs.ReadFile(staticFS, strings.TrimPrefix(src, "/static/"))
		if err != nil {
			return nil, err
		}
		data = b
	default:
		return nil, fmt.Errorf("unsupported logo source %q", src)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// ToDataURI decodes an uploaded image, downscales it so the long side is at most
// maxPx, re-encodes it as PNG, and returns a "data:image/png;base64,…" URI for
// storing in the DB. This keeps the logo small and fully offline.
func ToDataURI(src []byte, maxPx int) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("empty image")
	}
	if maxPx > 0 && (w > maxPx || h > maxPx) {
		nw, nh := maxPx, h*maxPx/w
		if h >= w {
			nw, nh = w*maxPx/h, maxPx
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// Logo scales the image to a fixed size (60% of the paper width, height capped at
// half the width), centers it on a full-width canvas, and returns an ESC/POS
// raster. The source size is irrelevant — output is always the same target size.
func Logo(img image.Image, canvasDots int) []byte {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	// Fixed target: width = 40% of the paper, height capped at 35% — so any
	// source resolution lands at the same modest printed size.
	newW := canvasDots * 40 / 100
	newH := sh * newW / sw
	if maxH := canvasDots * 35 / 100; newH > maxH {
		newH = maxH
		newW = sw * newH / sh
	}
	if newW <= 0 || newH <= 0 {
		return nil
	}
	scaled := image.NewGray(image.Rect(0, 0, newW, newH))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), img, b, xdraw.Src, nil)

	offX := (canvasDots - newW) / 2
	if offX < 0 {
		offX = 0
	}
	const threshold = 160 // grays darker than this become black dots
	return rasterFromInk(canvasDots, newH, func(x, y int) bool {
		lx := x - offX
		if lx < 0 || lx >= newW {
			return false
		}
		return scaled.GrayAt(lx, y).Y < threshold
	})
}
