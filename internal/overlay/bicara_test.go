package overlay

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// ask runs the chat up to the moment the answer arrives (fake clock).
func answerArrives(t *testing.T, p *Presenter, clk *fakeClock, reply func()) {
	t.Helper()
	p.OnHotkey(clk.now)
	p.Type([]rune("tanya"), clk.now)
	p.Submit(clk.now)
	p.Tick(clk.add(DefaultSlowAfter)) // mikir lama
	reply()
}

func TestSpeechDurationFollowsTextLength(t *testing.T) {
	p := &Presenter{}
	cases := map[int]time.Duration{
		0:    2 * time.Second, // never shorter than the minimum
		10:   2 * time.Second, // 400 ms → 2 s
		50:   2 * time.Second, // exactly 40 ms × 50
		75:   3 * time.Second, // 40 ms × 75
		150:  6 * time.Second,
		200:  8 * time.Second, // exactly the maximum
		2000: 8 * time.Second, // a long answer is typed faster, never slower
	}
	for n, want := range cases {
		if got := p.SpeechFor(n); got != want {
			t.Errorf("SpeechFor(%d) = %s, want %s", n, got, want)
		}
	}
	// the numbers come from config.yaml (speech.*)
	q := &Presenter{SpeechMsPerChar: 100 * time.Millisecond, SpeechMin: time.Second, SpeechMax: 4 * time.Second}
	if q.SpeechFor(5) != time.Second || q.SpeechFor(25) != 2500*time.Millisecond || q.SpeechFor(500) != 4*time.Second {
		t.Fatal("custom speech settings are ignored")
	}
	if r := (&Presenter{SpeechMin: 5 * time.Second, SpeechMax: time.Second}); r.SpeechFor(1) < 5*time.Second {
		t.Fatal("max below min must not shorten the minimum")
	}
}

// mikir → bicara (typed out, mouth moves) → normal, with the hop in between.
func TestBicaraGestureLifecycle(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{Form: forms.Biasa}
	text := strings.Repeat("Halo, ini jawaban. ", 6) // 114 chars: 4.56 s
	answerArrives(t, p, clk, func() { p.OnReply(text, clk.now) })
	arrived := clk.now
	total := utf8.RuneCountInString(text)
	d := p.SpeechFor(total)

	if l := p.Look(arrived.Add(HopFor / 2)); l.Form != forms.Senang || l.Hop == 0 {
		t.Fatalf("first the hop: %+v", l)
	}
	if p.Speaking(arrived.Add(HopFor - time.Millisecond)) {
		t.Fatal("not talking during the hop")
	}
	start := arrived.Add(HopFor)
	mid := p.Look(start.Add(d / 2))
	if mid.Form != forms.Bicara || mid.Env != 1 || mid.Talk < float64(total)/2-1 || mid.Talk > float64(total)/2+1 {
		t.Fatalf("mid-speech: %+v", mid)
	}
	if n := p.Revealed(start.Add(d / 2)); n != total/2 {
		t.Fatalf("text typed %d of %d at half time", n, total)
	}
	// the mouth really moves: it is not the same at two nearby moments
	opens := map[int]bool{}
	for i := 0; i < 10; i++ {
		opens[int(100*p.Look(start.Add(time.Duration(i)*130*time.Millisecond)).Talk)] = true
	}
	if len(opens) < 5 {
		t.Fatal("Talk (what the mouth follows) must advance with the letters")
	}
	// the gesture ends with the text plus a short closing nod, then back to the old form
	end := start.Add(d)
	if l := p.Look(end.Add(NodFor - time.Millisecond)); l.Form != forms.Bicara || l.Env > 0.1 {
		t.Fatalf("closing: %+v", l)
	}
	if l := p.Look(end.Add(NodFor)); l.Form != forms.Biasa {
		t.Fatalf("after the gesture the previous form returns: %+v", l)
	}
	p.Tick(end)
	if !p.Conversing() || p.Speaking(end.Add(NodFor)) {
		t.Fatal("text complete: the text box is open and nobody is talking")
	}
}

func TestBicaraStopsWhenTheOwnerActs(t *testing.T) {
	type stop struct {
		name string
		act  func(p *Presenter, now time.Time)
	}
	for _, s := range []stop{
		{"Enter skips the typing", func(p *Presenter, now time.Time) { p.SkipReveal(now) }},
		{"typing again", func(p *Presenter, now time.Time) { p.Type([]rune("a"), now) }},
		{"backspace", func(p *Presenter, now time.Time) { p.Backspace(now) }},
		{"Alt+Space", func(p *Presenter, now time.Time) { p.OnHotkey(now) }},
		{"Esc closes the bubble", func(p *Presenter, now time.Time) { p.CancelInput(now) }},
		{"click on Tunas", func(p *Presenter, now time.Time) { p.Dismiss(now) }},
	} {
		clk := &fakeClock{now: t0}
		p := &Presenter{Form: forms.Biasa}
		answerArrives(t, p, clk, func() { p.OnReply(strings.Repeat("kata ", 30), clk.now) })
		when := clk.now.Add(HopFor + time.Second)
		if !p.Speaking(when) {
			t.Fatalf("%s: should be talking before", s.name)
		}
		s.act(p, when)
		if p.Speaking(when) || p.Look(when).Form == forms.Bicara {
			t.Errorf("%s: the gesture must stop at once", s.name)
		}
		// and it never comes back later
		if p.Speaking(when.Add(500 * time.Millisecond)) {
			t.Errorf("%s: the gesture resumed", s.name)
		}
		checkOne(t, p)
	}
}

func TestIngatBeatsBicara(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{Form: forms.Biasa}
	answerArrives(t, p, clk, func() { p.OnReply(strings.Repeat("kata ", 30), clk.now) })
	when := clk.now.Add(HopFor + time.Second)
	if !p.Speaking(when) {
		t.Fatal("talking")
	}
	p.OnForm(reminder("minum obat"), when) // a reminder falls due mid-answer
	if p.Speaking(when) {
		t.Fatal("the reminder wins: Tunas stops talking")
	}
	if l := p.Look(when); l.Form != forms.Ingat {
		t.Fatalf("the character shows the reminder form: %+v", l)
	}
	if p.Revealed(when) != -1 || p.Queued() != 1 {
		t.Fatal("the answer is shown in full and the reminder is next")
	}
	checkOne(t, p)
}

func TestBicaraCariHasSourcesAfterTheText(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{Form: forms.Biasa}
	text := "Libur nasional 2027 ada 17 hari [1]."
	answerArrives(t, p, clk, func() { p.OnSearchReply(text, []string{"kompas.com", "detik.com"}, clk.now) })
	mid := clk.now.Add(HopFor + time.Second)
	if l := p.Look(mid); l.Form != forms.BicaraCari {
		t.Fatalf("search answers use bicara_cari: %+v", l)
	}
	if bt := p.BubbleText(); bt != text || strings.Contains(bt, "Sumber") {
		t.Fatalf("while typing only the text: %q", bt)
	}
	p.Tick(clk.now.Add(HopFor + p.SpeechFor(utf8.RuneCountInString(text))))
	want := text + "\n\nSumber: [1] kompas.com [2] detik.com\n› " + Cursor
	if bt := p.BubbleText(); bt != want {
		t.Fatalf("after the text:\n%q\nwant\n%q", bt, want)
	}
	// a plain answer never gets the magnifier or a source line
	q := &Presenter{}
	answerArrives(t, q, &fakeClock{now: t0}, func() { q.OnReply("biasa saja", t0) })
	if l := q.Look(t0.Add(HopFor + time.Second)); l.Form != forms.Bicara || strings.Contains(q.sourcesLine(), "Sumber") {
		t.Fatalf("plain answer: %+v", l)
	}
}

func TestBicaraNeedsAnimations(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{NoAnimations: true, Form: forms.Fokus}
	answerArrives(t, p, clk, func() { p.OnSearchReply("jawaban", []string{"a.id"}, clk.now) })
	if p.Speaking(clk.now.Add(time.Second)) || p.Look(clk.now.Add(time.Second)).Form != forms.Fokus {
		t.Fatal("animations: false → just the text")
	}
	if p.Revealed(clk.now) != -1 || !strings.Contains(p.BubbleText(), "Sumber: [1] a.id") {
		t.Fatalf("text and sources at once: %q", p.BubbleText())
	}
}

func TestNodAtSentenceEnds(t *testing.T) {
	text := []rune("Satu. Dua tiga empat. Lima")
	d := 2600 * time.Millisecond // 100 ms per character
	if nodAt(text, 3, d, 0) != 0 {
		t.Fatal("no nod in the middle of a sentence")
	}
	if n := nodAt(text, 5, d, 0); n != 0 { // 'Satu.' typed, 0 ms after the period
		t.Fatalf("the nod starts after the sentence end, got %v", n)
	}
	// 125 ms after the period is the middle of the nod
	if n := nodAt(text, 6+0, d, 0); n < 0.2 {
		t.Fatalf("right after the first sentence %v", n)
	}
	if n := nodAt(text, 14, d, 0); n != 0 { // 800 ms after: over
		t.Fatalf("nod must end after %s, got %v", NodFor, n)
	}
	if n := nodAt([]rune("Selesai."), 8, time.Second, NodFor/2); n < 0.99 {
		t.Fatalf("final nod when the text ends a sentence: %v", n)
	}
	if nodAt([]rune("tanpa titik"), 11, time.Second, NodFor/2) != 0 {
		t.Fatal("no final nod without a sentence end")
	}
}

func TestSourceNames(t *testing.T) {
	got := SourceNames([]agent.ChatSource{
		{URL: "https://www.kompas.com/a/b?x=1"}, {URL: "https://kompas.com/lain"}, // same site
		{URL: "https://news.detik.com/x"}, {URL: "bukan url"}, {URL: "http://bbc.co.uk/n"}, {URL: "https://lima.id"},
	})
	if strings.Join(got, ",") != "kompas.com,news.detik.com,bbc.co.uk" {
		t.Fatalf("source names = %v", got)
	}
}
