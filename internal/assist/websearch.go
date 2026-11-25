package assist

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/search"
)

// What the model gets from a search (PRD 6.11): at most 5 results, each
// only its title, url and the first 350 characters of the text, so the whole
// context stays under ~2.500 tokens even when Tavily returns thousands of
// characters per hit (free models have small per-minute token limits).
const (
	maxResults   = 5
	maxSnippet   = 350
	maxQueryLen  = 300
	answerBudget = 12 * time.Second // the model's answer; the web request has its own 8 s (12 s deep) limit
	minSources   = 2
	maxSources   = 3
	deepPrefix   = "dalam "
)

// Source is a result the answer points to.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// SearchReply is the outcome of /cari. Text is the answer; Sources are
// kept apart so Telegram can append them and the bubble can show them after
// the text has been typed out. Searched is true when the web was asked and
// answered (the bubble then uses the bicara_cari gesture).
type SearchReply struct {
	Text     string
	Sources  []Source
	Searched bool
}

// WithSources is the Telegram form: the answer and a numbered source list.
func (r SearchReply) WithSources() string {
	if len(r.Sources) == 0 {
		return r.Text
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Text) + "\n\nSumber:")
	for _, s := range r.Sources {
		fmt.Fprintf(&b, "\n%s", s.line())
	}
	return b.String()
}

func (s Source) line() string {
	if s.Title == "" {
		return s.URL
	}
	return s.Title + " — " + s.URL
}

// SearchText is /cari for Telegram.
func (a *Assistant) SearchText(ctx context.Context, args string) string {
	return a.Search(ctx, args).WithSources()
}

// Search answers /cari <kueri> (and "/cari dalam <kueri>" for the advanced
// depth, 2 credits). Code runs the search and writes the source list; the
// model only summarises the short snippets (no tool calling, so it works
// with any model). Every failure is answered, never silent.
func (a *Assistant) Search(ctx context.Context, args string) SearchReply {
	query := strings.TrimSpace(args)
	deep := false
	if rest, ok := cutPrefixFold(query, deepPrefix); ok {
		query, deep = strings.TrimSpace(rest), true
	}
	if query == "" {
		return SearchReply{Text: "Pakai: /cari <kueri>, mis. /cari jadwal libur nasional 2027\n(/cari dalam <kueri> untuk pencarian lebih dalam, 2 kredit)"}
	}
	if utf8.RuneCountInString(query) > maxQueryLen {
		return SearchReply{Text: fmt.Sprintf("Kuerinya terlalu panjang (maks %d karakter).", maxQueryLen)}
	}
	if a.Searcher == nil {
		return SearchReply{Text: msgSearchOff}
	}
	if a.Credits != nil {
		if done, u, lim, err := a.Credits.Exhausted(ctx); err == nil && done {
			a.Log.Info("web search skipped: monthly credits used up", "used", u.MonthUsed, "limit", lim)
			return SearchReply{Text: fmt.Sprintf("Jatah pencarian web bulan ini habis (%d/%d kredit). Jatahnya kembali di awal bulan depan.", u.MonthUsed, lim)}
		}
	}

	// Each step gets its own fresh budget from the caller's context: the web
	// request has its own limit (8 s, 12 s deep) inside the search client, the
	// model's answer gets answerBudget. They are not carved out of one shared
	// deadline, so a slow search cannot starve the answer, and an earlier step
	// cannot leave the search with an expired context.
	res, err := a.Searcher.Search(ctx, query, deep)
	a.Log.Info("web search", "ok", err == nil, "deep", deep, "results", len(res.Results), "credits", res.Credits,
		"ms", res.Elapsed.Milliseconds(), "err", errKind(err))
	if err != nil {
		return SearchReply{Text: searchErrorText(err)}
	}
	note := a.countCredits(ctx, res.Credits, deep)
	if len(res.Results) == 0 {
		return SearchReply{Text: "Tidak ada hasil yang relevan untuk: " + query + note, Searched: true}
	}
	actx, cancel := context.WithTimeout(ctx, answerBudget)
	defer cancel()
	text, sources := a.answerSearch(actx, query, res)
	return SearchReply{Text: strings.TrimSpace(text) + note, Sources: sources, Searched: true}
}

const msgSearchOff = "Pencarian web belum disetel (TAVILY_API_KEY kosong di .env server)."

// errKind is the error class for logs (never the key or the URL).
func errKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, search.ErrAuth):
		return "401"
	case errors.Is(err, search.ErrQuota):
		return "432/433"
	case errors.Is(err, search.ErrRate):
		return "429"
	case errors.Is(err, search.ErrBudget):
		return "budget_spent_before_send"
	case errors.Is(err, search.ErrTimeout):
		return "request_timeout"
	case errors.Is(err, search.ErrNetTimeout):
		return "network_timeout"
	case errors.Is(err, search.ErrDNS):
		return "dns"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var se *search.StatusError
	if errors.As(err, &se) {
		return strconv.Itoa(se.Code)
	}
	return "other"
}

// searchErrorText words each failure so the owner knows what to fix.
func searchErrorText(err error) string {
	var se *search.StatusError
	switch {
	case errors.Is(err, search.ErrDisabled):
		return msgSearchOff
	case errors.Is(err, search.ErrAuth):
		return "Kunci Tavily ditolak (401): salah, hilang, atau kedaluwarsa. Cek TAVILY_API_KEY di .env server."
	case errors.Is(err, search.ErrQuota):
		return "Kuota Tavily habis (432/433): batas paket atau batas bayar-sesuai-pakai terlewati. Cek dashboard Tavily."
	case errors.Is(err, search.ErrRate):
		return "Pencarian terlalu sering (429). Tunggu sebentar lalu coba lagi."
	case errors.Is(err, search.ErrBudget):
		return "Waktu permintaan sudah habis sebelum pencarian sempat dikirim. Coba lagi."
	case errors.Is(err, search.ErrNetTimeout):
		return "Tidak bisa tersambung ke layanan pencarian web (jaringan timeout). Coba lagi nanti."
	case errors.Is(err, search.ErrDNS):
		return "Alamat layanan pencarian web tidak bisa ditemukan (DNS gagal). Cek jaringan server."
	case errors.Is(err, search.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "Pencarian web terlalu lama menjawab. Coba lagi."
	case errors.As(err, &se):
		return fmt.Sprintf("Layanan pencarian web sedang bermasalah (%d). Coba lagi nanti.", se.Code)
	case errors.Is(err, context.Canceled):
		return "Pencarian dibatalkan."
	}
	return "Maaf, pencarian web sedang gagal. Coba lagi nanti."
}

// countCredits adds the credits of this request (what Tavily reported, else
// 1, or 2 for the advanced depth) and returns a note when the 80% mark was
// just crossed.
func (a *Assistant) countCredits(ctx context.Context, reported int, deep bool) string {
	if a.Credits == nil {
		return ""
	}
	n := reported
	if n <= 0 {
		n = 1
		if deep {
			n = 2
		}
	}
	u, lim, warn, err := a.Credits.Add(ctx, n)
	if err != nil {
		a.Log.Warn("search credits not saved", "err", err)
		return ""
	}
	if warn {
		return fmt.Sprintf("\n\n⚠️ Jatah pencarian bulan ini sudah terpakai %d/%d kredit (80%%).", u.MonthUsed, lim)
	}
	return ""
}

// compactResults is what the model may see: at most 5 results, each with
// title, url and the first maxSnippet characters.
func compactResults(rs []search.Result) []search.Result {
	if len(rs) > maxResults {
		rs = rs[:maxResults]
	}
	out := make([]search.Result, len(rs))
	for i, r := range rs {
		out[i] = search.Result{Title: oneLine(r.Title, 160), URL: r.URL, Content: oneLine(r.Content, maxSnippet)}
	}
	return out
}

// oneLine collapses whitespace and cuts to n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > n {
		return strings.TrimSpace(string(rs[:n])) + "…"
	}
	return s
}

// searchFacts is the model's whole context for one search.
func searchFacts(rs []search.Result) string {
	var b strings.Builder
	for i, r := range rs {
		fmt.Fprintf(&b, "[%d] %s\n%s\n%s\n\n", i+1, r.Title, r.URL, r.Content)
	}
	return b.String()
}

// answerSearch writes the answer in the configured mode. In llm mode a
// failing model falls back to the Tavily answer (no LLM).
func (a *Assistant) answerSearch(ctx context.Context, query string, res search.Response) (string, []Source) {
	rs := compactResults(res.Results)
	mode, err := search.AnswerMode(ctx, a.DB)
	if err != nil {
		a.Log.Warn("search answer mode not read", "err", err)
	}
	if mode == search.ModeLLM {
		if text, ok := a.llmSearchAnswer(ctx, query, rs); ok {
			return text, pickSources(text, rs)
		}
		a.Log.Info("search answer falls back to the Tavily answer")
	}
	return tavilyAnswer(res.Answer, rs), firstSources(rs, maxSources)
}

func (a *Assistant) llmSearchAnswer(ctx context.Context, query string, rs []search.Result) (string, bool) {
	f := a.ActiveForm(ctx)
	out, err := a.LLM.Complete(ctx, []llm.Message{
		{Role: "system", Content: a.voice(f) + "\nAturan: jawab dalam Bahasa Indonesia, maksimal 4 kalimat, HANYA dari hasil pencarian " +
			"di bawah. Sebut sumbernya dengan nomor seperti [1]. Bila hasilnya tidak menjawab atau kurang relevan, katakan terus terang; " +
			"jangan mengarang. Jangan meniru karakter dari film, game, atau anime mana pun."},
		{Role: "user", Content: "Pertanyaan: " + query + "\n\nHASIL PENCARIAN:\n" + searchFacts(rs)},
	})
	if err != nil {
		a.logLLMErr("search", err)
		return "", false
	}
	return out, strings.TrimSpace(out) != ""
}

// tavilyAnswer is the no-LLM answer: Tavily's own short answer, or the
// titles when it gave none.
func tavilyAnswer(answer string, rs []search.Result) string {
	if answer != "" {
		return answer
	}
	var b strings.Builder
	b.WriteString("Hasil pencarian:")
	for _, r := range firstN(rs, maxSources) {
		b.WriteString("\n• " + r.Title)
	}
	return b.String()
}

func firstN(rs []search.Result, n int) []search.Result {
	if len(rs) > n {
		return rs[:n]
	}
	return rs
}

func firstSources(rs []search.Result, n int) []Source {
	var out []Source
	for _, r := range firstN(rs, n) {
		out = append(out, Source{Title: r.Title, URL: r.URL})
	}
	return out
}

var reCite = regexp.MustCompile(`\[(\d+)\]`)

// pickSources lists 2 to 3 sources: the ones the answer cites, topped up
// with the first results. The model never writes the list (so a URL cannot
// be invented).
func pickSources(answer string, rs []search.Result) []Source {
	seen := map[int]bool{}
	var idx []int
	for _, m := range reCite.FindAllStringSubmatch(answer, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= len(rs) && !seen[n-1] {
			seen[n-1] = true
			idx = append(idx, n-1)
		}
	}
	sort.Ints(idx)
	for i := 0; i < len(rs) && len(idx) < minSources; i++ {
		if !seen[i] {
			seen[i] = true
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)
	if len(idx) > maxSources {
		idx = idx[:maxSources]
	}
	out := make([]Source, len(idx))
	for i, n := range idx {
		out[i] = Source{Title: rs[n].Title, URL: rs[n].URL}
	}
	return out
}

// Host is the site of a URL without "www.", for the compact source line in
// the bubble ("kompas.com").
func Host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// cutPrefixFold is strings.CutPrefix ignoring ASCII case.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}
