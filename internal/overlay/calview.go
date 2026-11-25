package overlay

import (
	"fmt"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/image/font"
)

// Calendar palette: dots and rings, no emoji (the bubble font has none).
var (
	colCalOK      = hexColor(0x3B6D11, 255) // a day with entries
	colCalMiss    = hexColor(0xC0392B, 255) // a past working day without entries
	colCalHoliday = hexColor(0xD85A30, 255)
	colCalLeave   = hexColor(0xBA7517, 255)
	colCalMuted   = hexColor(0x8A8880, 255)
	colCalSel     = hexColor(0xE3EFD0, 255)
)

func roundRectPath(x, y, w, h, r float32) *Path {
	k := r * (1 - kappa)
	var p Path
	p.MoveTo(x+r, y)
	p.LineTo(x+w-r, y)
	p.CubicTo(x+w-k, y, x+w, y+k, x+w, y+r)
	p.LineTo(x+w, y+h-r)
	p.CubicTo(x+w, y+h-k, x+w-k, y+h, x+w-r, y+h)
	p.LineTo(x+r, y+h)
	p.CubicTo(x+k, y+h, x, y+h-k, x, y+h-r)
	p.LineTo(x, y+r)
	p.CubicTo(x, y+k, x+k, y, x+r, y)
	p.Close()
	return &p
}

func ring(c *Canvas, cx, cy, r, width float32, col color.RGBA) {
	var p Path
	p.Ellipse(cx, cy, r, r)
	c.Stroke(&p, width, col)
}

func dot(c *Canvas, cx, cy, r float32, col color.RGBA) {
	var p Path
	p.Ellipse(cx, cy, r, r)
	c.Fill(&p, col)
}

// DrawCalendar draws the card at device scale s. measure is the width
// function of the 1x font; face is the font at scale s.
func DrawCalendar(c *Canvas, cal *Calendar, face font.Face, measure func(string) float64, s float64) {
	c.Clear()
	card := Rect{X: CalCardX, Y: CalCardY, W: CalCardW, H: CalCardH}
	bg := roundRectPath(float32(card.X*s), float32(card.Y*s), float32(card.W*s), float32(card.H*s), float32(BubbleRadius*s))
	c.Fill(bg, ColBubble)
	c.Stroke(bg, float32(1.5*s), ColBubbleInk)
	if cal.Mode == CalDayPanel {
		drawDayPanel(c, cal, face, measure, s)
		return
	}
	drawGrid(c, cal, face, measure, s)
}

func text(c *Canvas, face font.Face, s float64, str string, x, y float64, col color.RGBA) {
	c.Text(face, str, x*s, y*s, col)
}

// centered draws str centered in r.
func centered(c *Canvas, face font.Face, measure func(string) float64, s float64, str string, r Rect, col color.RGBA) {
	text(c, face, s, str, r.X+(r.W-measure(str))/2, r.Y+(r.H-LineHeight+6)/2, col)
}

func drawGrid(c *Canvas, cal *Calendar, face font.Face, measure func(string) float64, s float64) {
	l := cal.Layout()
	centered(c, face, measure, s, cal.Title(), l.Title, ColBubbleInk)
	text(c, face, s, "<", l.PrevHit.X+8, l.PrevHit.Y+3, ColLine)
	text(c, face, s, ">", l.NextHit.X+12, l.NextHit.Y+3, ColLine)
	for i, r := range l.Head {
		col := ColLine
		if i >= 5 {
			col = colCalMuted
		}
		centered(c, face, measure, s, CalHeads[i], r, col)
	}
	days := cal.DaysInMonth()
	for d := 1; d <= days; d++ {
		r := l.Cells[d]
		cx, cy := float32((r.X+r.W/2)*s), float32((r.Y+r.H/2)*s)
		rad := float32(11 * s)
		if d == cal.Sel {
			c.Fill(roundRectPath(float32(r.X*s)+float32(2*s), float32(r.Y*s)+float32(1*s), float32(r.W*s)-float32(4*s), float32(r.H*s)-float32(2*s), float32(6*s)), colCalSel)
		}
		ink := ColBubbleInk
		switch cal.MarkOf(d) {
		case MarkFilled:
			dot(c, cx, cy, rad, ColLeaf)
		case MarkMissed:
			ring(c, cx, cy, rad, float32(1.8*s), colCalMiss)
		case MarkHoliday:
			ink = colCalHoliday
			dot(c, cx, cy+float32(10*s), float32(2.2*s), colCalHoliday)
		case MarkLeave:
			ring(c, cx, cy, rad, float32(1.6*s), colCalLeave)
		case MarkWeekend:
			ink = colCalMuted
		}
		if cal.Month.Year() == cal.Today.Year() && cal.Month.Month() == cal.Today.Month() && d == cal.Today.Day() {
			ring(c, cx, cy, rad+float32(2*s), float32(2.2*s), ColBubbleInk)
		}
		centered(c, face, measure, s, fmt.Sprint(d), r, ink)
	}
	// footer: the summary, then the holiday name of the selected day or the note
	text(c, face, s, cal.Summary(), l.Footer.X, l.Footer.Y, ColBubbleInk)
	second := "Enter buka · T hari ini · PgUp/PgDn bulan · Esc"
	if n := cal.SelectedName(); n != "" {
		second = fmt.Sprintf("%d %s: %s", cal.Sel, calMonthNames[cal.Month.Month()-1][:3], n)
	}
	if cal.Note != "" && cal.Note != "data lama (offline)" {
		second = cal.Note
	}
	text(c, face, s, clip(second, measure, l.Footer.W), l.Footer.X, l.Footer.Y+LineHeight, colCalMuted)
}

// clip shortens str with an ellipsis so it fits maxW.
func clip(str string, measure func(string) float64, maxW float64) string {
	if measure(str) <= maxW {
		return str
	}
	rs := []rune(str)
	for len(rs) > 1 && measure(string(rs)+"…") > maxW {
		rs = rs[:len(rs)-1]
	}
	return string(rs) + "…"
}

var dayNamesID = [7]string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}

// DayTitle is "Rab 17 Nov 2026".
func DayTitle(d time.Time) string {
	return fmt.Sprintf("%s %d %s %d", dayNamesID[d.Weekday()], d.Day(), calMonthNames[d.Month()-1][:3], d.Year())
}

const (
	panelListLines = 6
	panelInputH    = 44
)

func drawDayPanel(c *Canvas, cal *Calendar, face font.Face, measure func(string) float64, s float64) {
	x0 := float64(CalCardX + calPad)
	w := float64(CalCardW - 2*calPad)
	y := float64(CalCardY + calPad)
	title := DayTitle(cal.SelDate())
	text(c, face, s, title, x0, y, ColBubbleInk)
	switch {
	case cal.Day != nil && cal.DayStale:
		text(c, face, s, "data lama", x0+w-measure("data lama"), y, colCalMuted)
	case cal.Busy:
		text(c, face, s, "...", x0+w-measure("..."), y, colCalMuted)
	}
	y += LineHeight
	if n := cal.SelectedName(); n != "" {
		text(c, face, s, clip(n, measure, w), x0, y, colCalHoliday)
		y += LineHeight - 4
	}
	listTop := y + 2
	listBottom := float64(CalCardY+CalCardH-calPad) - panelInputH - LineHeight
	lines := panelListLines
	if avail := int((listBottom - listTop) / LineHeight); avail < lines {
		lines = avail
	}
	drawEntries(c, cal, face, measure, s, x0, listTop, w, max(lines, 1))

	// note, input box, hint
	ny := listBottom + 2
	if cal.Note != "" {
		text(c, face, s, clip(cal.Note, measure, w), x0, ny, colCalMuted)
	}
	by := float64(CalCardY+CalCardH-calPad) - panelInputH - 14
	box := roundRectPath(float32(x0*s), float32(by*s), float32(w*s), float32(panelInputH*s), float32(6*s))
	border := ColLine
	if cal.Focus == FocusList {
		border = colCalMuted
	}
	c.Stroke(box, float32(1.3*s), border)
	label, draft := "Catatan baru:", cal.Draft
	if cal.Editing != 0 {
		label = "Ubah catatan:"
	}
	text(c, face, s, label, x0+6, by+3, colCalMuted)
	text(c, face, s, clip(inputPreview(draft)+cursor(cal), measure, w-12), x0+6, by+3+LineHeight-2, ColBubbleInk)
	hint := "Enter simpan · ↑ daftar · Esc kembali"
	if cal.Focus == FocusList {
		hint = "E ubah · Del hapus · ↓ ketik · Esc kembali"
	}
	if cal.ConfirmID != 0 {
		hint = "Hapus catatan ini? Enter = ya, lainnya batal"
	}
	text(c, face, s, clip(hint, measure, w), x0, by+panelInputH+1, colCalMuted)
}

func cursor(cal *Calendar) string {
	if cal.Focus == FocusInput && cal.ConfirmID == 0 {
		return "|"
	}
	return ""
}

// inputPreview shows the last line of the draft, and how many more lines
// (entries) a paste holds.
func inputPreview(d string) string {
	if d == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	if len(lines) == 1 {
		return tail(lines[0], 60)
	}
	return fmt.Sprintf("(%d baris) %s", len(lines), tail(lines[len(lines)-1], 40))
}

func tail(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)
	return "…" + string(rs[len(rs)-n:])
}

type entryBlock struct {
	lines []string
	idx   int
}

func drawEntries(c *Canvas, cal *Calendar, face font.Face, measure func(string) float64, s float64, x, y, w float64, maxLines int) {
	es := cal.entries()
	if len(es) == 0 {
		if cal.Day != nil && !cal.Busy {
			text(c, face, s, "Belum ada catatan.", x, y, colCalMuted)
		}
		return
	}
	var blocks []entryBlock
	for i, e := range es {
		ls := Wrap(fmt.Sprintf("%d. %s", i+1, e.Text), w-8, measure)
		if len(ls) > 2 {
			ls = append(ls[:1], clip(ls[1], measure, w-8-measure("…"))+"…")
		}
		blocks = append(blocks, entryBlock{ls, i})
	}
	// keep the selected entry in view: start at the first block that lets it fit
	start, used := 0, 0
	sel := 0
	if cal.Focus == FocusList {
		sel = cal.EntrySel
	} else {
		sel = len(es) - 1 // the newest entries are the ones just written
	}
	for start = 0; start < sel; start++ {
		used = 0
		for i := start; i <= sel; i++ {
			used += len(blocks[i].lines)
		}
		if used <= maxLines {
			break
		}
	}
	row := 0
	for i := start; i < len(blocks) && row < maxLines; i++ {
		b := blocks[i]
		if row+len(b.lines) > maxLines && row > 0 {
			break
		}
		if cal.Focus == FocusList && i == cal.EntrySel {
			c.Fill(roundRectPath(float32((x-2)*s), float32((y+float64(row)*LineHeight-1)*s), float32((w+4)*s), float32(float64(len(b.lines))*LineHeight*s), float32(4*s)), colCalSel)
		}
		for _, ln := range b.lines {
			if row == maxLines {
				break
			}
			text(c, face, s, ln, x, y+float64(row)*LineHeight, ColBubbleInk)
			row++
		}
	}
	if start > 0 {
		text(c, face, s, "↑", x+w-10, y, ColLine)
	}
}
