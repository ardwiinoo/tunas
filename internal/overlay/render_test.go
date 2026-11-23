package overlay

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/forms"
	"github.com/ardwiinoo/tunas/modes"
)

func TestRenderAllForms(t *testing.T) {
	face, err := NewFace(14 * 1.5)
	if err != nil {
		t.Fatal(err)
	}
	measureFace, _ := NewFace(14)
	measure := Measurer(measureFace)
	outDir := os.Getenv("TUNAS_RENDER_DIR") // set to write PNGs for visual review
	type look struct {
		name string
		f    forms.Form
		a    Anim
	}
	var looks []look
	for _, f := range forms.All {
		looks = append(looks, look{string(f), f, Anim{T: 0.5}})
	}
	looks = append(looks,
		look{"mikir", forms.Mikir, Anim{T: 0.5}},
		look{"mikir_lama", forms.Mikir, Anim{T: 4, Phase: 1}},
		look{"sangat_lama_x", forms.Mikir, Anim{T: 9.8, Phase: 2}}, // "x" eyes half of the time
		look{"sangat_lama", forms.Mikir, Anim{T: 9.1, Phase: 2}},
		look{"sedih", forms.Sedih, Anim{T: 1}},
		look{"berhasil_hop", forms.Senang, Anim{T: 10, Hop: 0.5}},
		look{"mikir_still", forms.Mikir, Anim{T: 0.5, Still: true}},
	)
	for _, lk := range looks {
		f := lk.name
		s := 1.5
		c := NewCanvas(int(WindowW*s), int(WindowH*s))
		sc := ComputeScene("bottom-right", "Halo, aku Tunas! Jangan lupa minum air putih ya.", measure)
		Frame(c, Procedural{}, sc, lk.f, face, lk.a, s)

		at := func(x, y float64) [4]uint8 {
			o := c.Img.PixOffset(int(x*s), int(y*s))
			p := c.Img.Pix[o : o+4]
			return [4]uint8{p[0], p[1], p[2], p[3]}
		}
		// transparent outside (click-through), body colour inside, bubble fill present
		if a := at(2, 2)[3]; a != 0 {
			t.Errorf("%s: corner alpha %d, want 0", f, a)
		}
		body := at(sc.Char.X+sc.Char.W/2, sc.Char.Y+sc.Char.H-14) // lower body, below the face
		if body != [4]uint8{ColBody.R, ColBody.G, ColBody.B, 255} {
			t.Errorf("%s: body pixel %v want %v", f, body, ColBody)
		}
		if bub := at(sc.Bubble.X+sc.Bubble.W-6, sc.Bubble.Y+sc.Bubble.H/2); bub[3] != 255 {
			t.Errorf("%s: bubble pixel %v", f, bub)
		}
		if outDir != "" {
			fh, _ := os.Create(filepath.Join(outDir, string(f)+".png"))
			_ = png.Encode(fh, c.Img)
			fh.Close()
		}
	}
}

func TestStrokeOverlapDoesNotCancel(t *testing.T) {
	c := NewCanvas(40, 40)
	var p Path
	p.MoveTo(5, 20)
	p.LineTo(35, 20)
	p.LineTo(5, 21) // sharp turn back over itself
	c.Stroke(&p, 4, ColFace)
	o := c.Img.PixOffset(20, 20)
	if c.Img.Pix[o+3] != 255 {
		t.Fatalf("overlapping stroke alpha %d", c.Img.Pix[o+3])
	}
}

// Fixed UI texts must only use characters the embedded font has; a missing
// glyph is drawn as a box.
func TestFixedTextsHaveGlyphs(t *testing.T) {
	face, err := NewFace(14)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{Cursor, "› ", "…", "↑", "↓", ConfirmHint, CancelHint, TextCancelled}
	ms, err := modes.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, ls := range ms["mikir"].Lines {
		texts = append(texts, ls...)
	}
	for _, ls := range [][]string{DefaultLoadingLines.Mikir, DefaultLoadingLines.MikirLama, DefaultLoadingLines.SangatLama} {
		texts = append(texts, ls...)
	}
	for _, f := range []agent.ChatFail{agent.FailTimeout, agent.FailOffline, agent.FailCap, agent.FailLLM,
		agent.FailDisabled, agent.FailAuth, agent.FailBusy, agent.FailServer,
		agent.FailBlocked, agent.FailQuota, agent.FailDown} {
		texts = append(texts, FailText(f))
	}
	for _, s := range texts {
		for _, r := range s {
			if _, ok := face.GlyphAdvance(r); !ok {
				t.Errorf("font has no glyph for %q (U+%04X) in %q", r, r, s)
			}
		}
	}
	// sanity: the check must see a glyph Go Regular lacks
	if _, ok := face.GlyphAdvance('▏'); ok {
		t.Fatal("GlyphAdvance no longer reports missing glyphs; this test proves nothing")
	}
}
