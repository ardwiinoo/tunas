package overlay

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/forms"
)

// mono measures 8px per rune.
func mono(s string) float64 { return float64(utf8.RuneCountInString(s)) * 8 }

func TestWrap(t *testing.T) {
	tests := []struct {
		in   string
		maxW float64
		want []string
	}{
		{"halo", 100, []string{"halo"}},
		{"satu dua tiga empat", 72, []string{"satu dua", "tiga", "empat"}},
		{"a\n\nb", 100, []string{"a", "", "b"}},
		{"supercalifragilistik", 64, []string{"supercal", "ifragili", "stik"}},
		{"ok supercalifragilistik", 64, []string{"ok", "supercal", "ifragili", "stik"}},
		{"  spasi   ganda  ", 200, []string{"spasi ganda"}},
		{"Jangan lupa minum air putih ya, sudah dua jam.", 120,
			[]string{"Jangan lupa", "minum air putih", "ya, sudah dua", "jam."}},
	}
	for _, tt := range tests {
		got := Wrap(tt.in, tt.maxW, mono)
		if strings.Join(got, "|") != strings.Join(tt.want, "|") {
			t.Errorf("Wrap(%q, %v) = %q, want %q", tt.in, tt.maxW, got, tt.want)
		}
		for _, l := range got {
			if mono(l) > tt.maxW {
				t.Errorf("line %q wider than %v", l, tt.maxW)
			}
		}
	}
}

func TestComputeScene(t *testing.T) {
	s := ComputeScene("bottom-right", "", mono)
	if s.Bubble.W != 0 || !s.Right || s.Char.X+s.Char.W > WindowW || s.Char.Y+s.Char.H != WindowH {
		t.Fatalf("no-text scene = %+v", s)
	}
	short := ComputeScene("bottom-right", "Zzz...", mono)
	long := ComputeScene("bottom-left", strings.Repeat("kata ", 200), mono)
	if short.Bubble.W >= long.Bubble.W || len(short.Lines) != 1 {
		t.Fatalf("bubble should fit text: short=%+v long=%+v", short.Bubble, long.Bubble)
	}
	if long.Bubble.W > BubbleMaxW || len(long.Lines) != MaxLines || !strings.HasSuffix(long.Lines[MaxLines-1], "…") {
		t.Fatalf("long bubble = %+v lines=%d", long.Bubble, len(long.Lines))
	}
	if long.Bubble.Y < 0 || long.Bubble.Y+long.Bubble.H > long.Char.Y {
		t.Fatalf("bubble must sit above the character inside the window: %+v", long.Bubble)
	}
	if long.Right || long.Bubble.X != long.Char.X {
		t.Fatal("left corner should left-align")
	}
	if short.TailX < short.Char.X || short.TailX > short.Char.X+short.Char.W {
		t.Fatal("tail must point at the character")
	}
	if !short.Char.Contains(short.Char.X+1, short.Char.Y+1) || short.Char.Contains(0, 0) {
		t.Fatal("Contains")
	}
}

func TestWindowPosition(t *testing.T) {
	for _, tt := range []struct {
		corner string
		x, y   int
	}{
		{"bottom-right", 1920 - WindowW - 24, 1080 - WindowH - 24},
		{"bottom-left", 24, 1080 - WindowH - 24},
		{"top-right", 1920 - WindowW - 24, 24},
		{"top-left", 24, 24},
	} {
		x, y := WindowPosition(tt.corner, 1920, 1080, 24, 24)
		if x != tt.x || y != tt.y {
			t.Errorf("%s = %d,%d want %d,%d", tt.corner, x, y, tt.x, tt.y)
		}
	}
	if x, y := WindowPosition("bottom-right", 200, 100, 24, 24); x != 0 || y != 0 {
		t.Error("tiny monitor should clamp to 0")
	}
}

func TestPresenter(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var p Presenter

	p.OnForm(forms.Output{Form: forms.Fokus}, t0)
	if p.Visible {
		t.Fatal("quiet form must not show")
	}
	p.OnForm(forms.Output{Form: forms.Tidur, Speech: "Zzz..."}, t0)
	if p.Visible {
		t.Fatal("tidur must not pop up")
	}
	p.OnForm(forms.Output{Form: forms.Ingat, Speech: "minum", Duration: 15 * time.Second}, t0)
	if !p.Visible || p.BubbleText() != "minum" {
		t.Fatalf("ingat = %+v", p)
	}
	p.Tick(t0.Add(14 * time.Second))
	if !p.Visible {
		t.Fatal("hid too early")
	}
	p.Tick(t0.Add(15 * time.Second))
	if p.Visible {
		t.Fatal("should hide after duration")
	}

	// hotkey → typing → submit → reply
	p.OnHotkey(t0)
	p.Type([]rune("halo\x08 tunas"), t0)
	p.Backspace(t0)
	if !p.Visible || p.Draft != "halo tuna" || !strings.HasPrefix(p.BubbleText(), "› halo tuna") {
		t.Fatalf("draft = %q", p.Draft)
	}
	p.OnForm(forms.Output{Form: forms.Ingat, Speech: "x", Duration: time.Second}, t0)
	if !p.Input {
		t.Fatal("form change must not interrupt typing")
	}
	p.Tick(t0.Add(InputTimeout - time.Second))
	if !p.Visible || !p.Input {
		t.Fatal("input mode hid while still within the timeout")
	}
	if got := p.Submit(t0); got != "halo tuna" || p.Input || p.BubbleText() != "..." {
		t.Fatalf("submit = %q %+v", got, p)
	}
	p.OnReply("Halo juga!", t0)
	p.Tick(t0.Add(7 * time.Second))
	if !p.Visible || p.BubbleText() != "Halo juga!" {
		t.Fatal("reply hidden too early")
	}
	p.Dismiss()
	if p.Visible {
		t.Fatal("dismiss")
	}

	// empty submit and toggle-cancel
	p.OnHotkey(t0)
	if p.Submit(t0) != "" || p.Visible {
		t.Fatal("empty submit should hide")
	}
	p.OnHotkey(t0)
	p.OnHotkey(t0)
	if p.Input || p.Visible {
		t.Fatal("second hotkey cancels")
	}

	p.OnHotkey(t0)
	p.Type([]rune(strings.Repeat("a", MaxDraftLen+50)), t0)
	if utf8.RuneCountInString(p.Draft) != MaxDraftLen {
		t.Fatal("draft not capped")
	}
	p.Type([]rune("b"), t0.Add(30*time.Second)) // activity extends the timeout
	p.Tick(t0.Add(80 * time.Second))
	if !p.Input {
		t.Fatal("typing should extend the input timeout")
	}
	p.Tick(t0.Add(91 * time.Second))
	if p.Input || p.Visible {
		t.Fatal("idle input should be cancelled after InputTimeout")
	}
}
