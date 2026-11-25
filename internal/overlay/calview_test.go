package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// testFontPx is the bubble font size (fontSize lives in the Windows-only file).
const testFontPx = 14

// The card must draw without a window: month grid and day panel render into
// a canvas, stay inside the card, and use only glyphs the font has.
func newCanvas(t *testing.T) (*Canvas, func(string) float64, float64) {
	t.Helper()
	face, err := NewFace(testFontPx)
	if err != nil {
		t.Fatal(err)
	}
	return NewCanvas(WindowW, WindowH), Measurer(face), 1
}

func opaqueBounds(c *Canvas) (minX, minY, maxX, maxY int, n int) {
	b := c.Img.Bounds()
	minX, minY, maxX, maxY = b.Max.X, b.Max.Y, -1, -1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c.Img.Pix[(y*b.Dx()+x)*4+3] != 0 {
				n++
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	return
}

func loadedMonth() *Calendar {
	c := calAt(2026, time.March, 18) // a six-row month
	days := make([]agent.CalDay, 31)
	for i := range days {
		days[i] = agent.CalDay{D: i + 1, Kind: "workday"}
	}
	days[2].Count = 2
	days[4].Kind, days[4].HolidayName = "holiday", "Hari Raya Nyepi"
	days[5].Kind = "weekend"
	c.SetMonth(agent.CalMonth{Month: "2026-03", Days: days, Workdays: 12, Filled: 10}, false)
	return c
}

func TestGridDrawsInsideTheCard(t *testing.T) {
	c, measure, s := newCanvas(t)
	face, _ := NewFace(testFontPx)
	cal := loadedMonth()
	cal.Sel = 5
	DrawCalendar(c, cal, face, measure, s)
	x0, y0, x1, y1, n := opaqueBounds(c)
	if n < 5000 {
		t.Fatalf("only %d pixels drawn", n)
	}
	if x0 < CalCardX-2 || y0 < CalCardY-2 || x1 > CalCardX+CalCardW+2 || y1 > CalCardY+CalCardH+2 {
		t.Fatalf("drawing %d,%d-%d,%d leaves the card %v", x0, y0, x1, y1, Rect{CalCardX, CalCardY, CalCardW, CalCardH})
	}
	// a second render of the same state is identical (deterministic)
	c2, _, _ := newCanvas(t)
	DrawCalendar(c2, cal, face, measure, s)
	if string(c.Img.Pix) != string(c2.Img.Pix) {
		t.Error("the same state drew differently")
	}
	// the selection changes the picture
	cal.Sel = 6
	c3, _, _ := newCanvas(t)
	DrawCalendar(c3, cal, face, measure, s)
	if string(c.Img.Pix) == string(c3.Img.Pix) {
		t.Error("moving the selection changed nothing")
	}
	// an unloaded month ("memuat...") and a stale one still draw
	loading := calAt(2026, time.November, 18)
	c4, _, _ := newCanvas(t)
	DrawCalendar(c4, loading, face, measure, s)
	if _, _, _, _, n := opaqueBounds(c4); n < 3000 {
		t.Errorf("loading card: %d px", n)
	}
	// at 150% scale the card is proportionally larger, still inside the canvas
	big := NewCanvas(int(WindowW*1.5), int(WindowH*1.5))
	bigFace, _ := NewFace(testFontPx * 1.5)
	DrawCalendar(big, cal, bigFace, measure, 1.5)
	if _, _, x1, y1, n := opaqueBounds(big); n < 10000 || x1 >= big.Img.Bounds().Dx() || y1 >= big.Img.Bounds().Dy() {
		t.Errorf("1.5x: %d px, extent %d,%d", n, x1, y1)
	}
}

func TestDayPanelDrawsEntriesAndInput(t *testing.T) {
	c, measure, s := newCanvas(t)
	face, _ := NewFace(testFontPx)
	cal := calAt(2026, time.November, 17)
	cal.Key(KeyEnter)
	long := strings.Repeat("perbaikan bug sinkronisasi ", 6)
	var es []agent.CalEntry
	for i := 0; i < 9; i++ {
		es = append(es, agent.CalEntry{ID: int64(i + 1), Text: long})
	}
	cal.SetDay(agent.CalDayView{Date: "2026-11-17", HolidayName: "", Entries: es}, false)
	cal.Type([]rune("catatan baru"))
	DrawCalendar(c, cal, face, measure, s)
	x0, y0, x1, y1, n := opaqueBounds(c)
	if n < 4000 || x0 < CalCardX-2 || y0 < CalCardY-2 || x1 > CalCardX+CalCardW+2 || y1 > CalCardY+CalCardH+2 {
		t.Fatalf("day panel: %d px at %d,%d-%d,%d", n, x0, y0, x1, y1)
	}
	// every state of the panel draws without panicking
	for name, setup := range map[string]func(*Calendar){
		"list focus":   func(c *Calendar) { c.Key(KeyUp) },
		"editing":      func(c *Calendar) { c.Key(KeyUp); c.Type([]rune("e")) },
		"confirm":      func(c *Calendar) { c.Key(KeyUp); c.Key(KeyDelete) },
		"multi-line":   func(c *Calendar) { c.Paste("a\nb\nc") },
		"note":         func(c *Calendar) { c.Done(CalAdd, nil, true) },
		"stale":        func(c *Calendar) { d := *c.Day; c.SetDay(d, true) },
		"empty day":    func(c *Calendar) { c.SetDay(agent.CalDayView{Date: "2026-11-17"}, false) },
		"holiday name": func(c *Calendar) { c.Data = &agent.CalMonth{Month: "2026-11", Days: []agent.CalDay{}} },
	} {
		cc := calAt(2026, time.November, 17)
		cc.Key(KeyEnter)
		cc.SetDay(agent.CalDayView{Date: "2026-11-17", Entries: es[:3]}, false)
		setup(cc)
		cv, _, _ := newCanvas(t)
		DrawCalendar(cv, cc, face, measure, s)
		if _, _, _, _, n := opaqueBounds(cv); n < 2000 {
			t.Errorf("%s: %d px", name, n)
		}
	}
	if got := DayTitle(date(2026, 11, 17)); got != "Sel 17 Nov 2026" {
		t.Errorf("day title %q", got)
	}
}

func TestCalendarTextsHaveGlyphs(t *testing.T) {
	face, err := NewFace(testFontPx)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{
		"Enter buka · T hari ini · PgUp/PgDn bulan · Esc",
		"Enter simpan · ↑ daftar · Esc kembali", "E ubah · Del hapus · ↓ ketik · Esc kembali",
		"Hapus catatan ini? Enter = ya, lainnya batal", "Catatan baru:", "Ubah catatan:", "data lama", "data lama (offline)",
		"Belum ada catatan.", "memuat...", "10/12 hari kerja terisi (data lama)", "<", ">", "|", "…",
		"Tersimpan", "Tersimpan di laptop, dikirim saat online", "Diubah", "Dihapus", "Gagal: server tidak terjangkau",
		"Sen Sel Rab Kam Jum Sab Min Januari Februari Maret April Mei Juni Juli Agustus September Oktober November Desember 0123456789",
	}
	for _, s := range texts {
		for _, r := range s {
			if _, ok := face.GlyphAdvance(r); !ok {
				t.Errorf("font has no glyph for %q (U+%04X) in %q", r, r, s)
			}
		}
	}
}

func TestClipAndPreview(t *testing.T) {
	face, _ := NewFace(testFontPx)
	m := Measurer(face)
	if got := clip("pendek", m, 200); got != "pendek" {
		t.Errorf("short: %q", got)
	}
	long := strings.Repeat("panjang ", 20)
	got := clip(long, m, 120)
	if !strings.HasSuffix(got, "…") || m(got) > 120 {
		t.Errorf("clip: %q (%.0f px)", got, m(got))
	}
	for in, want := range map[string]string{"": "", "satu": "satu", "a\nb\nc": "(3 baris) c", "a\nb\n": "(2 baris) b"} {
		if got := inputPreview(in); got != want {
			t.Errorf("inputPreview(%q) = %q, want %q", in, got, want)
		}
	}
}
