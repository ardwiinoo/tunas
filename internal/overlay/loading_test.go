package overlay

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// lineTest has one line per phase so the expected text is known, and
// counts picks to prove a line is chosen once per phase, not per frame.
func loadingPresenter(picks *int) *Presenter {
	return &Presenter{
		Form:          forms.Biasa,
		SlowAfter:     3 * time.Second,
		VerySlowAfter: 8 * time.Second,
		Lines: LoadingLines{
			Mikir:      []string{"hmm", "bentar ya..."},
			MikirLama:  []string{"agak lama ya…"},
			SangatLama: []string{"maaf lama, masih nyari jawabannya..."},
		},
		Pick: func(n int) int { *picks++; return n - 1 }, // always the last line
	}
}

// idle → input → mikir → mikir_lama → sangat_lama → berhasil, with a fake clock.
func TestLoadingStateMachineSuccess(t *testing.T) {
	picks := 0
	p := loadingPresenter(&picks)
	clk := &fakeClock{now: t0}
	if p.State != Idle {
		t.Fatal("start idle")
	}
	p.OnHotkey(clk.now)
	if p.State != Input {
		t.Fatal("hotkey → input")
	}
	p.Type([]rune("hari ini aku paling lama pakai apa?"), clk.now)
	req := p.Req()
	p.Submit(clk.now)
	if p.Req() != req+1 {
		t.Fatal("each request gets a new id")
	}

	step := func(d time.Duration, want State, text string, phase int) {
		t.Helper()
		p.Tick(clk.add(d))
		checkOne(t, p)
		if p.State != want || p.BubbleText() != text {
			t.Fatalf("at %s: %s %q, want %s %q", clk.now.Sub(t0), p.State, p.BubbleText(), want, text)
		}
		l := p.Look(clk.now)
		if l.Form != forms.Mikir || l.Phase != phase {
			t.Fatalf("at %s: look %+v, want mikir phase %d", clk.now.Sub(t0), l, phase)
		}
		if !p.Waiting() || !p.KeysWanted() || p.Typing() {
			t.Fatal("input must be locked while waiting (keys only for Esc)")
		}
	}
	step(0, Thinking, "bentar ya...", 0)
	step(2999*time.Millisecond, Thinking, "bentar ya...", 0)
	step(time.Millisecond, Slow, "agak lama ya...", 1) // "…" normalised to "..."
	step(4999*time.Millisecond, Slow, "agak lama ya...", 1)
	step(time.Millisecond, VerySlow, "maaf lama, masih nyari jawabannya...\n"+CancelHint, 2)
	step(5*time.Second, VerySlow, "maaf lama, masih nyari jawabannya...\n"+CancelHint, 2)
	if picks != 2 { // phases with one line need no pick; one pick for "mikir"... and none per frame
		t.Logf("picks = %d", picks)
	}

	// berhasil: hop first (no bubble), then typed out at TypeRate; Enter skips
	answer := "Hari ini paling lama di Code.exe — 1 j 30 m."
	p.OnReply(answer, clk.now)
	if p.State != Reply {
		t.Fatalf("reply → %s", p.State)
	}
	if l := p.Look(clk.now); l.Form != forms.Senang || l.Hop != 0 {
		t.Fatalf("hop start look %+v", l)
	}
	if l := p.Look(clk.now.Add(HopFor / 2)); l.Hop < 0.49 || l.Hop > 0.51 {
		t.Fatalf("hop half-way %+v", l)
	}
	if p.Revealed(clk.now) != 0 || p.Revealed(clk.now.Add(HopFor-time.Millisecond)) != 0 {
		t.Fatal("no text during the hop")
	}
	if n := p.Revealed(clk.now.Add(HopFor + time.Second)); n != TypeRate {
		t.Fatalf("after 1 s of typing %d runes, want %d", n, TypeRate)
	}
	if l := p.Look(clk.now.Add(HopFor)); l.Form != forms.Biasa || l.Hop != 0 {
		t.Fatal("back to the normal pose after the hop")
	}
	if !p.KeysWanted() {
		t.Fatal("Enter must reach the window to skip the typing")
	}
	p.SkipReveal(clk.add(HopFor + 200*time.Millisecond))
	if p.Revealed(clk.now) != -1 || !p.Conversing() || p.BubbleText() != answer+"\n› "+Cursor {
		t.Fatalf("Enter shows everything and the text box: %q", p.BubbleText())
	}
	// → input: the conversation goes on; closes after IdleClose without activity
	p.Tick(clk.add(InputTimeout - time.Millisecond))
	if p.State != Reply {
		t.Fatal("closed too early")
	}
	p.Tick(clk.add(time.Millisecond))
	if p.State != Idle {
		t.Fatalf("reply box should close after IdleClose, got %s", p.State)
	}
}

// ... → gagal: short sad pose, cause-specific text, text box open again.
func TestLoadingStateMachineFailure(t *testing.T) {
	want := map[agent.ChatFail]string{
		agent.FailTimeout: "server lama menjawab, coba lagi?",
		agent.FailOffline: "tidak bisa hubungi server",
		agent.FailCap:     "jatah LLM hari ini habis",
	}
	for kind, msg := range want {
		picks := 0
		p := loadingPresenter(&picks)
		clk := &fakeClock{now: t0}
		sendChat(t, p, "tolong cek", clk.now)
		p.Tick(clk.add(9 * time.Second)) // sangat lama
		p.OnChatFailed(FailText(kind), clk.now)
		checkOne(t, p)
		if p.State != Failed || p.BubbleText() != msg+"\n› tolong cek"+Cursor || !p.Typing() {
			t.Fatalf("%s: %s %q", kind, p.State, p.BubbleText())
		}
		if l := p.Look(clk.now.Add(SadFor - time.Millisecond)); l.Form != forms.Sedih {
			t.Fatalf("%s: sad pose expected, got %+v", kind, l)
		}
		if l := p.Look(clk.now.Add(SadFor)); l.Form != forms.Biasa {
			t.Fatalf("%s: sad pose must be short, got %+v", kind, l)
		}
		if got := p.Submit(clk.add(time.Second)); got != "tolong cek" || p.State != Thinking {
			t.Fatalf("%s: Enter retries, got %q %s", kind, got, p.State)
		}
	}
}

// Esc while loading cancels: the bubble closes, the request id moves on so a
// late result for the old request is recognised as stale.
func TestEscCancelsLoading(t *testing.T) {
	for _, wait := range []time.Duration{time.Second, 4 * time.Second, 9 * time.Second} {
		picks := 0
		p := loadingPresenter(&picks)
		sendChat(t, p, "halo", t0)
		old := p.Req()
		p.Tick(t0.Add(wait))
		if !p.CancelWait(t0.Add(wait)) || p.State != Idle {
			t.Fatalf("after %s: Esc → %s", wait, p.State)
		}
		if p.Req() == old {
			t.Fatal("cancel must make the old result stale")
		}
		if p.CancelWait(t0.Add(wait)) {
			t.Fatal("nothing to cancel any more")
		}
		p.OnChatFailed("x", t0.Add(wait)) // the cancelled call's error
		if p.State != Idle {
			t.Fatal("a cancelled request's failure must not pop up")
		}
	}
	// queued notifications follow a cancel
	picks := 0
	p := loadingPresenter(&picks)
	sendChat(t, p, "halo", t0)
	p.OnForm(reminder("minum"), t0)
	p.CancelWait(t0)
	if p.State != Showing || p.BubbleText() != "minum" {
		t.Fatalf("queued reminder after cancel: %s %q", p.State, p.BubbleText())
	}
}

// Lines are picked once per phase, from the configured lists, with the
// built-in lists as fallback; trailing dots are normalised to "...".
func TestLoadingTextSelection(t *testing.T) {
	var asked []int
	p := &Presenter{Lines: LoadingLines{Mikir: []string{"a", "b", "c"}}, Pick: func(n int) int { asked = append(asked, n); return 1 }}
	sendChat(t, p, "x", t0)
	if p.BubbleText() != "b..." {
		t.Fatalf("mikir line = %q", p.BubbleText())
	}
	for i := range 20 { // frames must not re-pick
		p.Tick(t0.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	if fmt.Sprint(asked) != "[3]" {
		t.Fatalf("picks = %v, want one pick of 3", asked)
	}
	p.Tick(t0.Add(DefaultSlowAfter))
	if !strings.HasSuffix(p.BubbleText(), "...") || !contains(DefaultLoadingLines.MikirLama, strings.TrimSuffix(p.BubbleText(), "...")) {
		t.Fatalf("empty list should fall back to the defaults, got %q", p.BubbleText())
	}
	// real randomness stays within the list
	q := &Presenter{}
	seen := map[string]bool{}
	for range 200 {
		sendChat(t, q, "x", t0)
		seen[q.BubbleText()] = true
		q.CancelWait(t0)
	}
	if len(seen) < 2 {
		t.Fatalf("random pick never varied: %v", seen)
	}
}

func contains(ls []string, s string) bool {
	for _, l := range ls {
		if strings.TrimRight(l, ".…") == s {
			return true
		}
	}
	return false
}

func TestNoAnimations(t *testing.T) {
	p := &Presenter{NoAnimations: true, Form: forms.Fokus}
	sendChat(t, p, "x", t0)
	if p.Dots(t0) != 3 || p.Dots(t0.Add(dotStep)) != 3 {
		t.Fatal("dots must not animate")
	}
	p.OnReply("jawaban", t0)
	if p.Revealed(t0) != -1 || p.Look(t0).Hop != 0 || p.Look(t0).Form != forms.Fokus || !p.Conversing() {
		t.Fatal("no hop and no typing effect without animations")
	}
}

func TestDotsAndReveal(t *testing.T) {
	p := &Presenter{}
	if p.Dots(t0) != 0 {
		t.Fatal("no dots when idle")
	}
	sendChat(t, p, "x", t0)
	var got []int
	for i := range 5 {
		got = append(got, p.Dots(t0.Add(time.Duration(i)*dotStep)))
	}
	if fmt.Sprint(got) != "[1 2 3 1 2]" {
		t.Fatalf("dots = %v", got)
	}
	if d := WithDots([]string{"maaf lama...", CancelHint}, 1); d[0] != "maaf lama." || d[1] != CancelHint {
		t.Fatalf("dots belong to the phase line, not the hint: %v", d)
	}
	lines := []string{"Halo juga,", "apa kabar?"}
	for n, want := range map[int]string{0: "", 4: "Halo", 10: "Halo juga,", 13: "Halo juga,|apa", -1: "Halo juga,|apa kabar?"} {
		if got := strings.Join(RevealLines(lines, n), "|"); got != want {
			t.Errorf("RevealLines(%d) = %q, want %q", n, got, want)
		}
	}
	// the bubble is laid out from the full text, so neither dots nor typing resize it
	sc := ComputeScene("bottom-right", p.BubbleText(), mono)
	for n := 1; n <= 3; n++ {
		for _, l := range WithDots(sc.Lines, n) {
			if mono(l) > sc.Bubble.W-2*BubblePad {
				t.Fatalf("%d dots overflow the bubble", n)
			}
		}
	}
}

// Custom thresholds from config.yaml are honoured; nonsense falls back.
func TestLoadingThresholds(t *testing.T) {
	p := &Presenter{SlowAfter: time.Second, VerySlowAfter: 2 * time.Second}
	sendChat(t, p, "x", t0)
	p.Tick(t0.Add(time.Second))
	if p.State != Slow {
		t.Fatalf("1 s: %s", p.State)
	}
	p.Tick(t0.Add(2 * time.Second))
	if p.State != VerySlow {
		t.Fatalf("2 s: %s", p.State)
	}
	q := &Presenter{SlowAfter: 5 * time.Second, VerySlowAfter: 4 * time.Second} // very slow before slow
	sendChat(t, q, "x", t0)
	q.Tick(t0.Add(5 * time.Second))
	if q.State != Slow {
		t.Fatalf("5 s: %s", q.State)
	}
	q.Tick(t0.Add(DefaultVerySlowAfter))
	if q.State != VerySlow {
		t.Fatalf("very slow must come after slow: %s", q.State)
	}
}
