package shared

import (
	"fmt"
	"math"
	"strings"
)

// Pure server-rendered SVG charts. Callers pass pre-computed float64 values
// (convert decimals first) plus pre-formatted display strings; the geometry is
// computed here so the .templ side just iterates ready-to-place primitives. No
// JavaScript — these render identically on screen and in the print → PDF path.

// ChartBar is one labelled value for the bar charts.
type ChartBar struct {
	Label string  // category / day label
	Value float64 // magnitude (negatives are clamped to 0 for width)
	Text  string  // pre-formatted value to show (e.g. "Rs 1,200"); falls back to the number
}

// ChartPoint is one point on a line series.
type ChartPoint struct {
	Label string
	Value float64
}

// LineSeries is one named line (e.g. "Revenue") with a colour.
type LineSeries struct {
	Name   string
	Color  string
	Values []float64
}

// ChartSlice is one wedge of the donut.
type ChartSlice struct {
	Label string
	Value float64
	Text  string
}

// Palette is the default categorical colour ramp (indigo, emerald, amber, rose,
// sky, violet, teal, orange) — reused across all charts.
var Palette = []string{
	"#6366f1", "#10b981", "#f59e0b", "#f43f5e",
	"#0ea5e9", "#8b5cf6", "#14b8a6", "#fb923c",
}

func paletteAt(i int) string { return Palette[i%len(Palette)] }

func barText(b ChartBar) string {
	if b.Text != "" {
		return b.Text
	}
	return trimFloat(b.Value)
}

func trimFloat(v float64) string {
	return fmt.Sprintf("%.0f", v)
}

func maxAbs(vals ...float64) float64 {
	m := 0.0
	for _, v := range vals {
		if a := math.Abs(v); a > m {
			m = a
		}
	}
	return m
}

// ---- Horizontal bars ----

// hbarRow is one positioned horizontal bar within a 760-wide viewBox.
type hbarRow struct {
	Y          float64
	BarW       float64
	Label      string
	Text       string
	Color      string
	LabelY     float64
	TextX      float64
	TextAnchor string
}

type hbarView struct {
	Width, Height float64
	GutterX       float64 // where bars start
	Rows          []hbarRow
}

const (
	hbarW      = 760.0
	hbarGutter = 180.0
	hbarRowH   = 30.0
	hbarGap    = 8.0
)

func buildHBars(bars []ChartBar) hbarView {
	v := hbarView{Width: hbarW, GutterX: hbarGutter}
	max := 0.0
	for _, b := range bars {
		if b.Value > max {
			max = b.Value
		}
	}
	if max <= 0 {
		max = 1
	}
	areaW := hbarW - hbarGutter - 90 // leave room for the value text on the right
	y := 6.0
	for i, b := range bars {
		val := b.Value
		if val < 0 {
			val = 0
		}
		bw := areaW * (val / max)
		if bw < 1 && b.Value > 0 {
			bw = 1
		}
		v.Rows = append(v.Rows, hbarRow{
			Y:          y,
			BarW:       bw,
			Label:      b.Label,
			Text:       barText(b),
			Color:      paletteAt(i),
			LabelY:     y + hbarRowH*0.68,
			TextX:      hbarGutter + bw + 6,
			TextAnchor: "start",
		})
		y += hbarRowH + hbarGap
	}
	v.Height = y + 4
	return v
}

// ---- Vertical bars ----

type vbarCol struct {
	X, Y, W, H float64
	Label      string
	Text       string
	Color      string
	LabelX     float64
}

type vbarView struct {
	Width, Height float64
	BaseY         float64
	Cols          []vbarCol
}

const (
	vbarW     = 800.0
	vbarH     = 260.0
	vbarBase  = 220.0 // y of the x-axis
	vbarTop   = 16.0
	vbarLeftX = 8.0
)

func buildVBars(bars []ChartBar) vbarView {
	v := vbarView{Width: vbarW, Height: vbarH, BaseY: vbarBase}
	if len(bars) == 0 {
		return v
	}
	max := 0.0
	for _, b := range bars {
		if b.Value > max {
			max = b.Value
		}
	}
	if max <= 0 {
		max = 1
	}
	areaH := vbarBase - vbarTop
	slot := (vbarW - vbarLeftX*2) / float64(len(bars))
	bw := slot * 0.6
	for i, b := range bars {
		val := b.Value
		if val < 0 {
			val = 0
		}
		h := areaH * (val / max)
		x := vbarLeftX + float64(i)*slot + (slot-bw)/2
		v.Cols = append(v.Cols, vbarCol{
			X:      x,
			Y:      vbarBase - h,
			W:      bw,
			H:      h,
			Label:  b.Label,
			Text:   barText(b),
			Color:  paletteAt(0),
			LabelX: x + bw/2,
		})
	}
	return v
}

// ---- Line chart ----

type linePath struct {
	Name   string
	Color  string
	Points string // "x,y x,y …" for <polyline>
	DotX   float64
	DotY   float64
}

type lineView struct {
	Width, Height float64
	BaseY         float64
	Series        []linePath
	Labels        []lineLabel
}

type lineLabel struct {
	X    float64
	Text string
}

const (
	lineW    = 800.0
	lineH    = 260.0
	lineBase = 220.0
	lineTop  = 16.0
	lineLeft = 8.0
)

// buildLine positions one or more series sharing the same x labels. The y axis is
// scaled to the max absolute value across every series so they're comparable.
func buildLine(labels []string, series []LineSeries) lineView {
	v := lineView{Width: lineW, Height: lineH, BaseY: lineBase}
	n := len(labels)
	if n == 0 {
		return v
	}
	max := 0.0
	for _, s := range series {
		max = math.Max(max, maxAbs(s.Values...))
	}
	if max <= 0 {
		max = 1
	}
	areaH := lineBase - lineTop
	areaW := lineW - lineLeft*2
	xAt := func(i int) float64 {
		if n == 1 {
			return lineLeft + areaW/2
		}
		return lineLeft + areaW*float64(i)/float64(n-1)
	}
	yAt := func(val float64) float64 {
		if val < 0 {
			val = 0
		}
		return lineBase - areaH*(val/max)
	}
	for si, s := range series {
		lp := linePath{Name: s.Name, Color: s.Color}
		if lp.Color == "" {
			lp.Color = paletteAt(si)
		}
		var pts strings.Builder
		for i, val := range s.Values {
			x, y := xAt(i), yAt(val)
			if i > 0 {
				pts.WriteByte(' ')
			}
			fmt.Fprintf(&pts, "%.1f,%.1f", x, y)
			lp.DotX, lp.DotY = x, y
		}
		lp.Points = pts.String()
		v.Series = append(v.Series, lp)
	}
	// Thin the x labels so they don't overlap (show ~8 max).
	step := 1
	if n > 8 {
		step = int(math.Ceil(float64(n) / 8))
	}
	for i, l := range labels {
		if i%step != 0 && i != n-1 {
			continue
		}
		v.Labels = append(v.Labels, lineLabel{X: xAt(i), Text: l})
	}
	return v
}

// ---- Donut ----

type donutArc struct {
	Color      string
	DashLen    float64
	DashOffset float64
	Label      string
	Text       string
	Pct        string
}

type donutView struct {
	Size, R, CX, CY, Stroke, Circ float64
	Empty                         bool
	Arcs                          []donutArc
}

const (
	donutSize   = 200.0
	donutR      = 80.0
	donutStroke = 32.0
)

// buildDonut lays out slices as stroke-dash segments on a single circle (rotated
// so the first slice starts at the top). Robust and print-safe — no arc maths.
func buildDonut(slices []ChartSlice) donutView {
	circ := 2 * math.Pi * donutR
	v := donutView{Size: donutSize, R: donutR, CX: donutSize / 2, CY: donutSize / 2, Stroke: donutStroke, Circ: circ}
	total := 0.0
	for _, s := range slices {
		if s.Value > 0 {
			total += s.Value
		}
	}
	if total <= 0 {
		v.Empty = true
		return v
	}
	acc := 0.0
	for i, s := range slices {
		if s.Value <= 0 {
			continue
		}
		frac := s.Value / total
		seg := frac * circ
		v.Arcs = append(v.Arcs, donutArc{
			Color:      paletteAt(i),
			DashLen:    seg,
			DashOffset: -acc,
			Label:      s.Label,
			Text:       s.Text,
			Pct:        fmt.Sprintf("%.0f%%", frac*100),
		})
		acc += seg
	}
	return v
}

// f formats a float for SVG attributes (1 dp, no trailing noise).
func f(v float64) string { return fmt.Sprintf("%.1f", v) }

// truncLabel keeps long category names from overrunning the bar gutter.
func truncLabel(s string) string {
	const max = 22
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
