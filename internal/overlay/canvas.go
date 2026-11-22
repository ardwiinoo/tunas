package overlay

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Canvas is a CPU-rasterized RGBA (premultiplied) drawing surface.
type Canvas struct {
	Img *image.RGBA
	z   *vector.Rasterizer
}

// NewCanvas creates a w×h transparent canvas.
func NewCanvas(w, h int) *Canvas {
	return &Canvas{Img: image.NewRGBA(image.Rect(0, 0, w, h)), z: vector.NewRasterizer(w, h)}
}

// Clear makes the canvas fully transparent.
func (c *Canvas) Clear() { clear(c.Img.Pix) }

type pt struct{ x, y float32 }

type segKind int

const (
	segMove segKind = iota
	segLine
	segQuad
	segCubic
	segClose
)

type seg struct {
	k   segKind
	pts [3]pt
}

// Path records vector segments for filling or stroking.
type Path struct {
	segs []seg
}

// MoveTo starts a subpath.
func (p *Path) MoveTo(x, y float32) { p.segs = append(p.segs, seg{k: segMove, pts: [3]pt{{x, y}}}) }

// LineTo adds a line.
func (p *Path) LineTo(x, y float32) { p.segs = append(p.segs, seg{k: segLine, pts: [3]pt{{x, y}}}) }

// QuadTo adds a quadratic Bézier.
func (p *Path) QuadTo(cx, cy, x, y float32) {
	p.segs = append(p.segs, seg{k: segQuad, pts: [3]pt{{cx, cy}, {x, y}}})
}

// CubicTo adds a cubic Bézier.
func (p *Path) CubicTo(c1x, c1y, c2x, c2y, x, y float32) {
	p.segs = append(p.segs, seg{k: segCubic, pts: [3]pt{{c1x, c1y}, {c2x, c2y}, {x, y}}})
}

// Close closes the current subpath.
func (p *Path) Close() { p.segs = append(p.segs, seg{k: segClose}) }

const kappa = 0.5523 // cubic Bézier circle approximation

// Ellipse adds a closed ellipse.
func (p *Path) Ellipse(cx, cy, rx, ry float32) {
	kx, ky := rx*kappa, ry*kappa
	p.MoveTo(cx+rx, cy)
	p.CubicTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubicTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubicTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubicTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
}

func (c *Canvas) draw(col color.RGBA) {
	c.z.DrawOp = draw.Over
	c.z.Draw(c.Img, c.Img.Bounds(), image.NewUniform(col), image.Point{})
}

// Fill fills p (nonzero/abs winding) with col (premultiplied).
func (c *Canvas) Fill(p *Path, col color.RGBA) {
	b := c.Img.Bounds()
	c.z.Reset(b.Dx(), b.Dy())
	open := false
	for _, s := range p.segs {
		switch s.k {
		case segMove:
			if open {
				c.z.ClosePath()
			}
			c.z.MoveTo(s.pts[0].x, s.pts[0].y)
			open = true
		case segLine:
			c.z.LineTo(s.pts[0].x, s.pts[0].y)
		case segQuad:
			c.z.QuadTo(s.pts[0].x, s.pts[0].y, s.pts[1].x, s.pts[1].y)
		case segCubic:
			c.z.CubeTo(s.pts[0].x, s.pts[0].y, s.pts[1].x, s.pts[1].y, s.pts[2].x, s.pts[2].y)
		case segClose:
			c.z.ClosePath()
			open = false
		}
	}
	if open {
		c.z.ClosePath()
	}
	c.draw(col)
}

// flatten converts the path into polylines (one per subpath).
func (p *Path) flatten() [][]pt {
	const steps = 12
	var out [][]pt
	var cur []pt
	var start, last pt
	flush := func() {
		if len(cur) > 1 {
			out = append(out, cur)
		}
		cur = nil
	}
	for _, s := range p.segs {
		switch s.k {
		case segMove:
			flush()
			start, last = s.pts[0], s.pts[0]
			cur = []pt{last}
		case segLine:
			last = s.pts[0]
			cur = append(cur, last)
		case segQuad:
			p0, p1, p2 := last, s.pts[0], s.pts[1]
			for i := 1; i <= steps; i++ {
				t := float32(i) / steps
				u := 1 - t
				cur = append(cur, pt{u*u*p0.x + 2*u*t*p1.x + t*t*p2.x, u*u*p0.y + 2*u*t*p1.y + t*t*p2.y})
			}
			last = p2
		case segCubic:
			p0, p1, p2, p3 := last, s.pts[0], s.pts[1], s.pts[2]
			for i := 1; i <= steps; i++ {
				t := float32(i) / steps
				u := 1 - t
				a, b, cc, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
				cur = append(cur, pt{a*p0.x + b*p1.x + cc*p2.x + d*p3.x, a*p0.y + b*p1.y + cc*p2.y + d*p3.y})
			}
			last = p3
		case segClose:
			cur = append(cur, start)
			last = start
			flush()
		}
	}
	flush()
	return out
}

// Stroke draws p with width w and round joins/caps.
func (c *Canvas) Stroke(p *Path, w float32, col color.RGBA) {
	b := c.Img.Bounds()
	c.z.Reset(b.Dx(), b.Dy())
	h := w / 2
	for _, line := range p.flatten() {
		for i := 0; i+1 < len(line); i++ {
			a, e := line[i], line[i+1]
			dx, dy := e.x-a.x, e.y-a.y
			l := float32(math.Hypot(float64(dx), float64(dy)))
			if l == 0 {
				continue
			}
			nx, ny := -dy/l*h, dx/l*h
			// same orientation for every quad so overlaps add up instead of cancelling
			c.z.MoveTo(a.x+nx, a.y+ny)
			c.z.LineTo(e.x+nx, e.y+ny)
			c.z.LineTo(e.x-nx, e.y-ny)
			c.z.LineTo(a.x-nx, a.y-ny)
			c.z.ClosePath()
		}
		for _, q := range line { // round joins and caps
			c.circle(q.x, q.y, h)
		}
	}
	c.draw(col)
}

// circle adds a circle to the rasterizer with the same orientation as stroke quads.
func (c *Canvas) circle(cx, cy, r float32) {
	// Quads have negative signed area (see Stroke), so trace the circle the
	// same way; opposite windings would cancel where they overlap.
	k := r * kappa
	c.z.MoveTo(cx+r, cy)
	c.z.CubeTo(cx+r, cy-k, cx+k, cy-r, cx, cy-r)
	c.z.CubeTo(cx-k, cy-r, cx-r, cy-k, cx-r, cy)
	c.z.CubeTo(cx-r, cy+k, cx-k, cy+r, cx, cy+r)
	c.z.CubeTo(cx+k, cy+r, cx+r, cy+k, cx+r, cy)
	c.z.ClosePath()
}

// Text draws s with its top-left at (x, y).
func (c *Canvas) Text(face font.Face, s string, x, y float64, col color.RGBA) {
	m := face.Metrics()
	d := font.Drawer{Dst: c.Img, Src: image.NewUniform(col), Face: face,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y*64) + m.Ascent}}
	d.DrawString(s)
}

var goFont *opentype.Font

// NewFace returns the embedded Go Regular font (BSD-3) at size px.
func NewFace(px float64) (font.Face, error) {
	if goFont == nil {
		f, err := opentype.Parse(goregular.TTF)
		if err != nil {
			return nil, err
		}
		goFont = f
	}
	return opentype.NewFace(goFont, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
}

// Measurer returns a width function for face (in pixels).
func Measurer(face font.Face) func(string) float64 {
	return func(s string) float64 { return float64(font.MeasureString(face, s)) / 64 }
}
