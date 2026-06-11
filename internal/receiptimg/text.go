package receiptimg

import (
	"bytes"
	_ "embed"
	"image"
	"math"
	"strings"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Embedded fonts (OFL) so non-Latin shop names print without relying on system
// fonts — keeps the static binary self-contained.
//
//go:embed fonts/NotoSansSinhala-Regular.ttf
var sinhalaTTF []byte

//go:embed fonts/NotoSansTamil-Regular.ttf
var tamilTTF []byte

var (
	sinhalaFace = mustFace(sinhalaTTF)
	tamilFace   = mustFace(tamilTTF)
)

func mustFace(b []byte) *font.Face {
	f, err := font.ParseTTF(bytes.NewReader(b))
	if err != nil {
		panic("receiptimg: parse font: " + err.Error())
	}
	return f
}

// pickFace chooses the embedded font + shaping script from the text's content.
// Noto Sans Sinhala also covers basic Latin, so it's the Latin fallback too.
func pickFace(text string) (*font.Face, language.Script, language.Language) {
	for _, r := range text {
		switch {
		case r >= 0x0B80 && r <= 0x0BFF:
			return tamilFace, language.Tamil, language.NewLanguage("ta")
		case r >= 0x0D80 && r <= 0x0DFF:
			return sinhalaFace, language.Sinhala, language.NewLanguage("si")
		}
	}
	return sinhalaFace, language.Latin, language.NewLanguage("en")
}

// SubName shapes text (Sinhala/Tamil/Latin) at pxHeight pixels tall, centers it
// on a canvasDots-wide canvas, and returns an ESC/POS raster. Returns nil for
// empty text.
func SubName(text string, canvasDots, pxHeight int) []byte {
	img := renderText(text, canvasDots, pxHeight)
	if img == nil {
		return nil
	}
	h := img.Bounds().Dy()
	return rasterFromInk(canvasDots, h, func(x, y int) bool {
		return img.AlphaAt(x, y).A > 96
	})
}

// renderText shapes and rasterizes text to an alpha image (ink = high alpha),
// centered on a canvasDots-wide canvas. Returns nil for empty text.
func renderText(text string, canvasDots, pxHeight int) *image.Alpha {
	text = strings.TrimSpace(text)
	if text == "" || pxHeight <= 0 {
		return nil
	}
	face, script, lang := pickFace(text)
	runes := []rune(text)

	shaper := shaping.HarfbuzzShaper{}
	out := shaper.Shape(shaping.Input{
		Text:      runes,
		RunStart:  0,
		RunEnd:    len(runes),
		Direction: di.DirectionLTR,
		Face:      face,
		Size:      fixed.I(pxHeight),
		Script:    script,
		Language:  lang,
	})
	if len(out.Glyphs) == 0 {
		return nil
	}

	scale := float32(pxHeight) / float32(face.Upem())
	ascent := f26(out.LineBounds.Ascent)
	descent := f26(out.LineBounds.Descent) // negative
	height := int(math.Ceil(float64(ascent-descent))) + 4
	width := int(math.Ceil(float64(f26(out.Advance)))) + 4
	if width > canvasDots {
		width = canvasDots
	}
	baseline := ascent + 2
	startX := float32((canvasDots-width)/2) + 2
	if startX < 0 {
		startX = 0
	}

	rast := vector.NewRasterizer(canvasDots, height)
	penX := startX
	for _, g := range out.Glyphs {
		if outline, ok := face.GlyphData(g.GlyphID).(font.GlyphOutline); ok {
			ox := penX + f26(g.XOffset)
			oy := baseline - f26(g.YOffset)
			for _, s := range outline.Segments {
				switch s.Op {
				case ot.SegmentOpMoveTo:
					rast.MoveTo(ox+s.Args[0].X*scale, oy-s.Args[0].Y*scale)
				case ot.SegmentOpLineTo:
					rast.LineTo(ox+s.Args[0].X*scale, oy-s.Args[0].Y*scale)
				case ot.SegmentOpQuadTo:
					rast.QuadTo(
						ox+s.Args[0].X*scale, oy-s.Args[0].Y*scale,
						ox+s.Args[1].X*scale, oy-s.Args[1].Y*scale)
				case ot.SegmentOpCubeTo:
					rast.CubeTo(
						ox+s.Args[0].X*scale, oy-s.Args[0].Y*scale,
						ox+s.Args[1].X*scale, oy-s.Args[1].Y*scale,
						ox+s.Args[2].X*scale, oy-s.Args[2].Y*scale)
				}
			}
			rast.ClosePath()
		}
		penX += f26(g.Advance)
	}

	dst := image.NewAlpha(image.Rect(0, 0, canvasDots, height))
	rast.Draw(dst, dst.Bounds(), image.Opaque, image.Point{})
	return dst
}

func f26(v fixed.Int26_6) float32 { return float32(v) / 64 }
