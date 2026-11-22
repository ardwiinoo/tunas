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
	T        float64 // seconds since the form started
	Motion   float64 // short random idle animation in [0,1] (0 = none)
	Thinking bool    // waiting for a chat reply: the leaf sways slowly
	Think    float64 // seconds since the chat was sent
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
	rx, ry := 44*sc, 38*sc

	angle := leafAngle(f, t, motion)
	if a.Thinking {
		angle += thinkSway(a.Think)
	}
	drawLeaf(c, cx, cy-ry+2*sc, angle, sc, lw)

	var body Path
	body.Ellipse(cx, cy, rx, ry)
	c.Fill(&body, ColBody)
	c.Stroke(&body, lw, ColLine)

	var ch Path
	ch.Ellipse(cx-24*sc, cy+8*sc, 7*sc, 4.5*sc)
	ch.Ellipse(cx+24*sc, cy+8*sc, 7*sc, 4.5*sc)
	c.Fill(&ch, ColCheek)

	drawFace(c, f, cx, cy, sc, lw, t, motion)
}

// thinkSway is the slow pendulum of the leaf while Tunas is thinking
// (about one swing every 2.4 s, ±0.25 rad).
func thinkSway(t float64) float64 { return math.Sin(t*2.6) * 0.25 }

func leafAngle(f forms.Form, t, motion float64) float64 {
	switch f {
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

func drawLeaf(c *Canvas, x, y float32, angle float64, sc, lw float32) {
	sin, cos := float32(math.Sin(angle)), float32(math.Cos(angle))
	rot := func(px, py float32) (float32, float32) { return x + px*cos - py*sin, y + px*sin + py*cos }

	var stem Path
	sx, sy := rot(0, -10*sc)
	stem.MoveTo(x, y)
	stem.LineTo(sx, sy)
	c.Stroke(&stem, lw, ColLine)

	var leaf Path
	tx, ty := rot(0, -34*sc)
	l1x, l1y := rot(-13*sc, -22*sc)
	r1x, r1y := rot(13*sc, -22*sc)
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

func drawFace(c *Canvas, f forms.Form, cx, cy, sc, lw float32, t, motion float64) {
	ex, ey := 15*sc, -4*sc
	blink := f == forms.Biasa && (math.Mod(t, 4.2) < 0.14 || (motion > 0.85))
	var p Path
	switch {
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
	case forms.Tegur:
		m.MoveTo(cx-6*sc, my+2*sc)
		m.LineTo(cx+6*sc, my-1*sc)
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
	}
}
