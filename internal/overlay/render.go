package overlay

import (
	"image/color"
	"math"

	"golang.org/x/image/font"

	"github.com/ardwiinoo/tunas/internal/forms"
)

// Renderer draws the character. The procedural renderer can later be
// replaced by a sprite-sheet renderer without touching the state logic.
type Renderer interface {
	// DrawCharacter draws form f inside r (logical px) with animation
	// clocks a at device scale s.
	DrawCharacter(c *Canvas, f forms.Form, r Rect, a Anim, s float64)
}

// Anim carries the animation clocks for one frame.
type Anim struct {
	T      float64 // seconds since the form (or display form) started
	Motion float64 // short random idle animation in [0,1] (0 = none)
	Phase  int     // forms.Mikir: 0 mikir, 1 mikir lama, 2 sangat lama
	Hop    float64 // success hop progress in (0,1), 0 = none
	Still  bool    // animations off: static poses
	Talk   float64 // forms.Bicara: characters typed so far (the mouth follows)
	Nod    float64 // forms.Bicara: nod at the end of a sentence, 0..1
	Env    float64 // forms.Bicara: 1 while talking, fading to 0 as the mouth closes
}

// Palette from PRD 11.1 (premultiplied RGBA).
var (
	ColBody      = hexColor(0xC0DD97, 255)
	ColLine      = hexColor(0x3B6D11, 255)
	ColLeaf      = hexColor(0x97C459, 255)
	ColFace      = hexColor(0x173404, 255)
	ColCheek     = hexColor(0xF0997B, 140) // 55% opacity
	ColBubble    = hexColor(0xF7F5EE, 255)
	ColBubbleInk = hexColor(0x2C2C2A, 255)
)

func hexColor(v uint32, a uint8) color.RGBA {
	f := float64(a) / 255
	pm := func(c uint8) uint8 { return uint8(math.Round(float64(c) * f)) }
	return color.RGBA{pm(uint8(v >> 16)), pm(uint8(v >> 8)), pm(uint8(v)), a}
}

// Procedural draws Tunas with vector paths (no image files).
type Procedural struct{}

// DrawCharacter implements Renderer.
func (Procedural) DrawCharacter(c *Canvas, f forms.Form, r Rect, a Anim, s float64) {
	t, motion := a.T, a.Motion
	if a.Still {
		t, motion = 10, 0 // past every one-off animation, no idle wiggle
	}
	size := 1.0
	if f == forms.Fokus {
		size = 0.92
	}
	sc := float32(s * size)
	lw := float32(1.5 * s)

	cx := float32((r.X + r.W/2) * s)
	cy := float32((r.Y + r.H - 40) * s)
	if f == forms.Senang && t < 5 { // small hops for 5 s
		cy -= float32(math.Abs(math.Sin(t*math.Pi*2.5)) * 10 * s)
	}
	if f == forms.Tidur { // slow breathing
		cy += float32(math.Sin(t*1.5) * 1.2 * s)
	}
	if a.Hop > 0 && !a.Still { // one small hop on success
		cy -= float32(math.Sin(a.Hop*math.Pi) * 9 * s)
	}
	if f == forms.Mikir && a.Phase >= 1 && !a.Still { // slow side-to-side sway
		cx += float32(math.Sin(t*2.4) * 3 * s)
	}
	talking := (f == forms.Bicara || f == forms.BicaraCari) && !a.Still
	if talking { // a small nod at the end of a sentence
		cy += float32(a.Nod * 3 * s)
	}
	rx, ry := 44*sc, 38*sc

	spin := float32(1)
	if f == forms.Mikir && a.Phase < 2 && !a.Still {
		// propeller: the leaf turns around its stem, so seen from the front
		// its width shrinks, flips and grows again (it never hides behind the body)
		spin = float32(math.Cos(t * math.Pi * 1.2))
	}
	drawLeaf(c, cx, cy-ry+2*sc, leafAngle(f, a.Phase, t, motion, a.Still), spin, sc, lw)

	var body Path
	body.Ellipse(cx, cy, rx, ry)
	c.Fill(&body, ColBody)
	c.Stroke(&body, lw, ColLine)

	var ch Path
	ch.Ellipse(cx-24*sc, cy+8*sc, 7*sc, 4.5*sc)
	ch.Ellipse(cx+24*sc, cy+8*sc, 7*sc, 4.5*sc)
	c.Fill(&ch, ColCheek)

	if f == forms.Mikir && a.Phase >= 1 {
		drawSweat(c, cx+rx*0.8, cy-ry*0.45, sc, lw)
	}
	mouth := 0.0 // how open the mouth is while talking, 0..1
	if talking {
		mouth = a.Env * math.Abs(math.Sin(a.Talk*math.Pi/2.4))
	}
	drawFace(c, f, a.Phase, cx, cy, sc, lw, t, motion, a.Still, mouth)
	if f == forms.BicaraCari {
		side := float32(1) // the magnifier goes on the side with room
		if r.X+r.W/2 > WindowW/2 {
			side = -1
		}
		drawMagnifier(c, cx+side*rx*0.82, cy-ry*0.62, side, sc, lw)
	}
}

// drawMagnifier is a small magnifying glass beside the head: a lens with a
// glint and a short handle pointing away from the body.
func drawMagnifier(c *Canvas, x, y, side, sc, lw float32) {
	r := 7.5 * sc
	var lens Path
	lens.Ellipse(x, y, r, r)
	c.Fill(&lens, ColBubble)
	c.Stroke(&lens, lw*1.2, ColLine)
	var glint Path
	glint.MoveTo(x-r*0.55, y-r*0.1)
	glint.QuadTo(x-r*0.5, y-r*0.5, x-r*0.1, y-r*0.55)
	c.Stroke(&glint, lw*0.9, ColLeaf)
	var handle Path
	hx, hy := x+side*r*0.72, y-r*0.72
	handle.MoveTo(hx, hy)
	handle.LineTo(hx+side*8*sc, hy-8*sc)
	c.Stroke(&handle, lw*2.2, ColLine)
}

// drawSweat draws a small drop on the side of the body.
func drawSweat(c *Canvas, x, y, sc, lw float32) {
	var d Path
	d.MoveTo(x, y-6*sc)
	d.QuadTo(x+4.5*sc, y+1*sc, x, y+3.5*sc)
	d.QuadTo(x-4.5*sc, y+1*sc, x, y-6*sc)
	d.Close()
	c.Fill(&d, ColBubble)
	c.Stroke(&d, lw*0.8, ColLine)
}

func leafAngle(f forms.Form, phase int, t, motion float64, still bool) float64 {
	switch f {
	case forms.Mikir:
		if phase >= 2 {
			return 0.9 + math.Sin(t*1.5)*0.05 // drooping halfway
		}
		if still {
			return 0.5
		}
		return 0.15 // upright; the propeller spin is the leaf's width (see spin)
	case forms.Sedih:
		return 0.5 + 1.0*math.Min(t/0.4, 1) // wilts over 0.4 s
	case forms.Bicara, forms.BicaraCari:
		if still {
			return 0.5
		}
		return 0.5 + math.Sin(t*5)*0.10 // a small sway while talking
	case forms.Fokus, forms.Ingat, forms.Tegur:
		return 0
	case forms.Tidur:
		return 1.75 // droops sideways
	case forms.Senang:
		if t < 1 {
			return t * 2 * math.Pi // one full spin
		}
		return 0.5
	}
	// biasa: tilted up-right, gentle sway; motion adds a wiggle
	return 0.5 + math.Sin(t*1.3)*0.08 + math.Sin(motion*math.Pi*6)*0.35*motion
}

// drawLeaf draws the leaf at angle; spin in [-1,1] scales its width (1 = flat
// facing the viewer, 0 = edge-on, negative = flipped).
func drawLeaf(c *Canvas, x, y float32, angle float64, spin, sc, lw float32) {
	sin, cos := float32(math.Sin(angle)), float32(math.Cos(angle))
	rot := func(px, py float32) (float32, float32) { return x + px*cos - py*sin, y + px*sin + py*cos }

	var stem Path
	sx, sy := rot(0, -10*sc)
	stem.MoveTo(x, y)
	stem.LineTo(sx, sy)
	c.Stroke(&stem, lw, ColLine)

	var leaf Path
	tx, ty := rot(0, -34*sc)
	hw := 13 * sc * spin
	if hw > -1.5*sc && hw < 1.5*sc { // keep a sliver when edge-on
		hw = 1.5 * sc
	}
	l1x, l1y := rot(-hw, -22*sc)
	r1x, r1y := rot(hw, -22*sc)
	leaf.MoveTo(sx, sy)
	leaf.QuadTo(l1x, l1y, tx, ty)
	leaf.QuadTo(r1x, r1y, sx, sy)
	leaf.Close()
	c.Fill(&leaf, ColLeaf)
	c.Stroke(&leaf, lw, ColLine)

	var vein Path
	vx, vy := rot(0, -28*sc)
	vein.MoveTo(sx, sy)
	vein.LineTo(vx, vy)
	c.Stroke(&vein, lw*0.6, ColLine)
}

func drawFace(c *Canvas, f forms.Form, phase int, cx, cy, sc, lw float32, t, motion float64, still bool, mouth float64) {
	ex, ey := 15*sc, -4*sc
	blink := f == forms.Biasa && (math.Mod(t, 4.2) < 0.14 || (motion > 0.85))
	crossed := f == forms.Mikir && phase >= 2 && !still && math.Mod(t, 1.2) < 0.6
	var p Path
	switch {
	case crossed: // small "x" eyes, alternating with normal ones
		for _, sx := range []float32{-1, 1} {
			x0, y0, k := cx+sx*ex, cy+ey, 3.5*sc
			p.MoveTo(x0-k, y0-k)
			p.LineTo(x0+k, y0+k)
			p.MoveTo(x0+k, y0-k)
			p.LineTo(x0-k, y0+k)
		}
		c.Stroke(&p, lw*1.3, ColFace)
	case f == forms.Mikir: // glancing up and to the right
		r := 4 * sc
		p.Ellipse(cx-ex+2.5*sc, cy+ey-3*sc, r, r)
		p.Ellipse(cx+ex+2.5*sc, cy+ey-3*sc, r, r)
		c.Fill(&p, ColFace)
	case f == forms.Sedih: // looking down, sad brows (inner ends raised)
		r := 3.5 * sc
		p.Ellipse(cx-ex, cy+ey+3*sc, r, r)
		p.Ellipse(cx+ex, cy+ey+3*sc, r, r)
		c.Fill(&p, ColFace)
		var b Path
		b.MoveTo(cx-ex-6*sc, cy+ey-6*sc)
		b.LineTo(cx-ex+5*sc, cy+ey-10*sc)
		b.MoveTo(cx+ex+6*sc, cy+ey-6*sc)
		b.LineTo(cx+ex-5*sc, cy+ey-10*sc)
		c.Stroke(&b, lw*1.2, ColFace)
	case blink, f == forms.Fokus:
		for _, sx := range []float32{-1, 1} {
			p.MoveTo(cx+sx*ex-5*sc, cy+ey)
			p.LineTo(cx+sx*ex+5*sc, cy+ey)
		}
		c.Stroke(&p, lw*1.6, ColFace)
	case f == forms.Tidur: // closed arcs
		for _, sx := range []float32{-1, 1} {
			p.MoveTo(cx+sx*ex-6*sc, cy+ey-1*sc)
			p.QuadTo(cx+sx*ex, cy+ey+5*sc, cx+sx*ex+6*sc, cy+ey-1*sc)
		}
		c.Stroke(&p, lw*1.4, ColFace)
	case f == forms.Senang: // raised arcs ^ ^
		for _, sx := range []float32{-1, 1} {
			p.MoveTo(cx+sx*ex-6*sc, cy+ey+2*sc)
			p.QuadTo(cx+sx*ex, cy+ey-6*sc, cx+sx*ex+6*sc, cy+ey+2*sc)
		}
		c.Stroke(&p, lw*1.4, ColFace)
	default: // dots (big for ingat)
		r := 4 * sc
		if f == forms.Ingat {
			r = 6 * sc
		}
		p.Ellipse(cx-ex, cy+ey, r, r)
		p.Ellipse(cx+ex, cy+ey, r, r)
		c.Fill(&p, ColFace)
		if f == forms.Tegur { // slanted brows
			var b Path
			b.MoveTo(cx-ex-6*sc, cy+ey-11*sc)
			b.LineTo(cx-ex+5*sc, cy+ey-8*sc)
			b.MoveTo(cx+ex+6*sc, cy+ey-11*sc)
			b.LineTo(cx+ex-5*sc, cy+ey-8*sc)
			c.Stroke(&b, lw*1.3, ColFace)
		}
	}

	my := cy + 12*sc
	var m Path
	switch f {
	case forms.Fokus:
		m.MoveTo(cx-6*sc, my)
		m.LineTo(cx+6*sc, my)
		c.Stroke(&m, lw*1.3, ColFace)
	case forms.Tidur:
		m.Ellipse(cx, my+1*sc, 3*sc, 3*sc)
		c.Stroke(&m, lw, ColFace)
	case forms.Senang:
		m.MoveTo(cx-12*sc, my-2*sc)
		m.QuadTo(cx, my+12*sc, cx+12*sc, my-2*sc)
		m.QuadTo(cx, my+4*sc, cx-12*sc, my-2*sc)
		m.Close()
		c.Fill(&m, ColFace)
	case forms.Ingat:
		m.Ellipse(cx, my+1*sc, 4*sc, 3.2*sc)
		c.Fill(&m, ColFace)
	case forms.Bicara, forms.BicaraCari: // opens and closes with the typed letters
		if mouth < 0.12 {
			m.MoveTo(cx-5*sc, my)
			m.LineTo(cx+5*sc, my)
			c.Stroke(&m, lw*1.3, ColFace)
		} else {
			m.Ellipse(cx, my+1*sc, 4.4*sc, float32(1.2+4.6*mouth)*sc)
			c.Fill(&m, ColFace)
		}
	case forms.Tegur:
		m.MoveTo(cx-6*sc, my+2*sc)
		m.LineTo(cx+6*sc, my-1*sc)
		c.Stroke(&m, lw*1.3, ColFace)
	case forms.Mikir: // small "o"
		m.Ellipse(cx+1*sc, my+1*sc, 2.6*sc, 2.6*sc)
		c.Stroke(&m, lw*1.1, ColFace)
	case forms.Sedih: // crooked, turned down
		m.MoveTo(cx-7*sc, my+3*sc)
		m.QuadTo(cx-1*sc, my-2*sc, cx+6*sc, my+1*sc)
		c.Stroke(&m, lw*1.3, ColFace)
	default:
		m.MoveTo(cx-6*sc, my-1*sc)
		m.QuadTo(cx, my+5*sc, cx+6*sc, my-1*sc)
		c.Stroke(&m, lw*1.3, ColFace)
	}
}

// DrawBubble draws a rounded speech bubble whose tail points down at tailX.
func DrawBubble(c *Canvas, b Rect, tailX, s float64) {
	x, y, w, h := float32(b.X*s), float32(b.Y*s), float32(b.W*s), float32(b.H*s)
	r := float32(BubbleRadius * s)
	k := r * (1 - kappa)
	tw := float32(9 * s)
	th := float32(TailH * s)
	tx := min(max(float32(tailX*s), x+r+tw), x+w-r-tw)

	var p Path
	p.MoveTo(x+r, y)
	p.LineTo(x+w-r, y)
	p.CubicTo(x+w-k, y, x+w, y+k, x+w, y+r)
	p.LineTo(x+w, y+h-r)
	p.CubicTo(x+w, y+h-k, x+w-k, y+h, x+w-r, y+h)
	p.LineTo(tx+tw, y+h)
	p.LineTo(tx+tw*0.2, y+h+th)
	p.LineTo(tx-tw, y+h)
	p.LineTo(x+r, y+h)
	p.CubicTo(x+k, y+h, x, y+h-k, x, y+h-r)
	p.LineTo(x, y+r)
	p.CubicTo(x, y+k, x+k, y, x+r, y)
	p.Close()
	c.Fill(&p, ColBubble)
	c.Stroke(&p, float32(1.5*s), ColBubbleInk)
}

// Frame renders a whole overlay frame: character, bubble and text.
func Frame(c *Canvas, r Renderer, sc Scene, f forms.Form, face font.Face, a Anim, s float64) {
	c.Clear()
	r.DrawCharacter(c, f, sc.Char, a, s)
	if sc.Bubble.W > 0 {
		DrawBubble(c, sc.Bubble, sc.TailX, s)
		for i, line := range sc.Lines {
			c.Text(face, line, (sc.Bubble.X+BubblePad)*s, (sc.Bubble.Y+BubblePad+float64(i)*LineHeight+1)*s, ColBubbleInk)
		}
		// scroll markers in the right gutter (↑/↓, PageUp/PageDown or the wheel)
		mx := (sc.Bubble.X + sc.Bubble.W - BubblePad - ScrollGutter + 4) * s
		if sc.MoreAbove {
			c.Text(face, "↑", mx, (sc.Bubble.Y+BubblePad+1)*s, ColLine)
		}
		if sc.MoreBelow {
			c.Text(face, "↓", mx, (sc.Bubble.Y+BubblePad+float64(len(sc.Lines)-1)*LineHeight+1)*s, ColLine)
		}
	}
}
