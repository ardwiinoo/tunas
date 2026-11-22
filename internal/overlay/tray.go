package overlay

import (
	"image/color"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// Tray texts and colours (platform-independent so they can be tested).

// TrayTip is the tray tooltip for a connection state.
func TrayTip(s agent.ConnState, paused bool) string {
	t := "Tunas: "
	switch s {
	case agent.ConnOnline:
		t += "tersambung"
	case agent.ConnConnecting:
		t += "menyambung ulang…"
	default:
		t += "offline"
	}
	if paused {
		t += " (pelacakan dijeda)"
	}
	return t
}

// TrayColor is the status dot colour: green, yellow, red.
func TrayColor(s agent.ConnState) color.RGBA {
	switch s {
	case agent.ConnOnline:
		return color.RGBA{0x3F, 0xB9, 0x50, 0xFF}
	case agent.ConnConnecting:
		return color.RGBA{0xE3, 0xB3, 0x41, 0xFF}
	}
	return color.RGBA{0xE5, 0x53, 0x4B, 0xFF}
}

// Tray menu items.
const (
	MenuPause = "Jeda pelacakan"
	MenuLog   = "Buka log"
	MenuQuit  = "Keluar"
)

// TrayIcon draws the status dot as straight-alpha BGRA pixels (the layout
// CreateIconIndirect expects for a 32-bit icon), size×size.
func TrayIcon(s agent.ConnState, size int) []byte {
	c := NewCanvas(size, size)
	col := TrayColor(s)
	r := float32(size) * 0.36
	cx := float32(size) / 2
	var p Path
	p.Ellipse(cx, cx, r, r)
	c.Fill(&p, col)
	c.Stroke(&p, max(1, float32(size)/16), color.RGBA{0x20, 0x20, 0x20, 0xFF})
	src := c.Img.Pix
	out := make([]byte, len(src))
	for i := 0; i+3 < len(src); i += 4 {
		a := src[i+3]
		if a == 0 {
			continue
		}
		un := func(v byte) byte { return byte(min(255, int(v)*255/int(a))) } // un-premultiply
		out[i], out[i+1], out[i+2], out[i+3] = un(src[i+2]), un(src[i+1]), un(src[i]), a
	}
	return out
}
