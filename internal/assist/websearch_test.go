package assist

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/search"
)

// fakeTavily stands in for the Tavily client.
type fakeTavily struct {
	res   search.Response
	err   error
	calls int
	q     string
	deep  bool
}

func (f *fakeTavily) Search(_ context.Context, q string, deep bool) (search.Response, error) {
	f.calls++
	f.q, f.deep = q, deep
	return f.res, f.err
}

// tenBig is what Tavily can return: 10 results with thousands of characters.
func tenBig() search.Response {
	var rs []search.Result
	for i := 1; i <= 10; i++ {
		rs = append(rs, search.Result{Title: fmt.Sprintf("Judul hasil %d", i), URL: fmt.Sprintf("https://situs%d.id/artikel", i),
			Content: strings.Repeat(fmt.Sprintf("kalimat%d yang sangat panjang. ", i), 400)}) // ~12.000 chars each
	}
	return search.Response{Answer: "Ringkasan dari Tavily (English).", Results: rs, Credits: 1}
}

func searchRig(t *testing.T, f *fakeLLM, ft *fakeTavily) (*Assistant, *search.Credits) {
	a, db, now := setup(t, f)
	cr := &search.Credits{DB: db, Now: func() time.Time { return *now }, Loc: jkt}
	a.Searcher, a.Credits = ft, cr
	return a, cr
}

// A huge response must not reach the model: 5 results at most, each title +
// url + ≤ 350 characters, the whole context under ~2.500 tokens.
func TestSearchContextIsBounded(t *testing.T) {
	f := &fakeLLM{replies: []string{"Jawabannya ada di [2] dan [4]."}}
	a, _ := searchRig(t, f, &fakeTavily{res: tenBig()})
	r := a.Search(context.Background(), "apa kabar berita hari ini")
	if len(f.seen) != 1 {
		t.Fatalf("%d LLM calls", len(f.seen))
	}
	user := f.seen[0][1].Content
	if n := utf8.RuneCountInString(user); n > 2800 { // ~2.500 tokens even at 1 token per char
		t.Fatalf("context is %d characters", n)
	}
	if strings.Count(user, "https://situs") != 5 || strings.Contains(user, "[6]") || strings.Contains(user, "Judul hasil 6") {
		t.Fatalf("more than 5 results reached the model:\n%s", user)
	}
	for _, blk := range strings.Split(user, "\n\n")[1:] {
		if lines := strings.Split(blk, "\n"); len(lines) >= 4 && utf8.RuneCountInString(lines[3]) > maxSnippet+1 {
			t.Fatalf("snippet of %d characters", utf8.RuneCountInString(lines[3]))
		}
	}
	if strings.Contains(user, "Ringkasan dari Tavily") || strings.Contains(f.seen[0][0].Content, "yang sangat panjang") {
		t.Fatal("only title, url and snippet go to the model")
	}
	// sources: the cited ones, written by code, 2 to 3
	if len(r.Sources) != 2 || r.Sources[0].URL != "https://situs2.id/artikel" || r.Sources[1].URL != "https://situs4.id/artikel" {
		t.Fatalf("sources = %+v", r.Sources)
	}
	if !r.Searched || !strings.HasPrefix(r.WithSources(), "Jawabannya ada di [2] dan [4].\n\nSumber:\nJudul hasil 2 — https://situs2.id/artikel") {
		t.Fatalf("telegram text %q", r.WithSources())
	}
}

func TestSearchSourcesAreAlwaysTwoOrThree(t *testing.T) {
	rs := compactResults(tenBig().Results)
	for answer, want := range map[string]int{
		"tanpa kutipan": 2, "lihat [3]": 2, "[1] [2] [3] [4] [5]": 3, "[9] salah nomor": 2, "[2] [2] [2]": 2,
	} {
		if got := pickSources(answer, rs); len(got) != want {
			t.Errorf("%q → %d sources, want %d", answer, len(got), want)
		}
	}
	if got := pickSources("[1]", rs[:1]); len(got) != 1 {
		t.Error("one result gives one source")
	}
}

func TestSearchTavilyMode(t *testing.T) {
	f := &fakeLLM{replies: []string{"tidak dipakai"}}
	ft := &fakeTavily{res: tenBig()}
	a, _ := searchRig(t, f, ft)
	if err := a.DB.SetSetting(context.Background(), search.KeyAnswerMode, "tavily"); err != nil {
		t.Fatal(err)
	}
	r := a.Search(context.Background(), "berita")
	if len(f.seen) != 0 {
		t.Fatal("tavily mode must not call the model (it saves the quota)")
	}
	if r.Text != "Ringkasan dari Tavily (English)." || len(r.Sources) != 3 || r.Sources[0].URL != "https://situs1.id/artikel" {
		t.Fatalf("reply %+v", r)
	}
}

func TestSearchFallsBackToTavilyWhenLLMFails(t *testing.T) {
	a, _ := searchRig(t, &fakeLLM{err: errors.New("down")}, &fakeTavily{res: tenBig()})
	r := a.Search(context.Background(), "berita")
	if r.Text != "Ringkasan dari Tavily (English)." || len(r.Sources) != 3 {
		t.Fatalf("fallback %+v", r)
	}
	// no Tavily answer either: titles from code
	res := tenBig()
	res.Answer = ""
	a2, _ := searchRig(t, &fakeLLM{err: errors.New("down")}, &fakeTavily{res: res})
	if r := a2.Search(context.Background(), "berita"); !strings.HasPrefix(r.Text, "Hasil pencarian:\n• Judul hasil 1") || len(r.Sources) != 3 {
		t.Fatalf("titles fallback %+v", r)
	}
	// an empty model answer is a failure too
	a3, _ := searchRig(t, &fakeLLM{replies: []string{"   "}}, &fakeTavily{res: tenBig()})
	if r := a3.Search(context.Background(), "berita"); r.Text != "Ringkasan dari Tavily (English)." {
		t.Fatalf("empty answer %+v", r)
	}
}

// Numbers in web text are third-party facts: the model may repeat them. (The
// guard that rejects invented numbers is for the owner's own data.)
func TestSearchKeepsNumbersFromTheWeb(t *testing.T) {
	res := search.Response{Results: []search.Result{{Title: "Harga", URL: "https://a.id/x", Content: "Harga naik 12,5% menjadi Rp 15.000"}}}
	a, _ := searchRig(t, &fakeLLM{replies: []string{"Harga naik sekitar 12 persen [1]."}}, &fakeTavily{res: res})
	if r := a.Search(context.Background(), "harga"); !strings.HasPrefix(r.Text, "Harga naik sekitar 12 persen [1].") {
		t.Fatalf("a normal answer was thrown away: %q", r.Text)
	}
}

func TestSearchErrorMessages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{search.ErrAuth, "401"},
		{search.ErrQuota, "432/433"},
		{search.ErrRate, "429"},
		{search.ErrTimeout, "terlalu lama"},
		{&search.StatusError{Code: 502}, "(502)"},
		{errors.New("dial tcp: boom"), "gagal"},
		{search.ErrDisabled, "TAVILY_API_KEY"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		a, _ := searchRig(t, &fakeLLM{}, &fakeTavily{err: c.err})
		got := a.Search(context.Background(), "q").Text
		if !strings.Contains(got, c.want) || seen[got] {
			t.Errorf("%v → %q (want %q, and a message of its own)", c.err, got, c.want)
		}
		seen[got] = true
	}
	// no key configured at all
	a, _, _ := setup(t, &fakeLLM{})
	if got := a.Search(context.Background(), "q").Text; got != msgSearchOff {
		t.Fatalf("no searcher → %q", got)
	}
	// no results: honest, nothing invented, no sources
	a, _ = searchRig(t, &fakeLLM{}, &fakeTavily{})
	if r := a.Search(context.Background(), "zzzz"); !strings.HasPrefix(r.Text, "Tidak ada hasil yang relevan untuk: zzzz") || len(r.Sources) != 0 {
		t.Fatalf("no results → %+v", r)
	}
	if got := a.Search(context.Background(), "   ").Text; !strings.Contains(got, "Pakai:") {
		t.Fatal("usage")
	}
	if got := a.Search(context.Background(), strings.Repeat("a", maxQueryLen+1)).Text; !strings.Contains(got, "terlalu panjang") {
		t.Fatal("long query")
	}
}

func TestSearchCredits(t *testing.T) {
	ctx := context.Background()
	ft := &fakeTavily{res: search.Response{Results: []search.Result{{Title: "A", URL: "https://a.id", Content: "isi"}}, Credits: 1}}
	a, cr := searchRig(t, &fakeLLM{replies: []string{"a [1]", "b [1]", "c [1]", "d [1]", "e [1]"}}, ft)
	if err := a.DB.SetSetting(ctx, search.KeyLimit, 5); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for range 4 {
		texts = append(texts, a.Search(ctx, "q").Text)
	}
	// the 4th credit is 80% of 5: warned once, in that reply only
	for i, tx := range texts {
		if has := strings.Contains(tx, "80%"); has != (i == 3) {
			t.Fatalf("reply %d warning=%v: %q", i, has, tx)
		}
	}
	// "dalam" = advanced = 2 credits when Tavily does not say how many
	ft.res.Credits = 0
	r := a.Search(ctx, "dalam kueri mendalam")
	if ft.q != "kueri mendalam" || !ft.deep {
		t.Fatalf("deep search: %q deep=%v", ft.q, ft.deep)
	}
	_ = r
	if u, _, _ := cr.State(ctx); u.MonthUsed != 6 {
		t.Fatalf("credits used: %d, want 4+2", u.MonthUsed)
	}
	// used up: answered without calling the API again
	calls := ft.calls
	got := a.Search(ctx, "q").Text
	if ft.calls != calls || !strings.Contains(got, "Jatah pencarian web bulan ini habis (6/5") {
		t.Fatalf("exhausted: %d calls, %q", ft.calls-calls, got)
	}
}
