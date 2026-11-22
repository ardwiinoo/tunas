package assist

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/search"
)

// maxSnippet bounds each result's text passed to the model (runes).
const maxSnippet = 600

// Search answers /cari: code runs the web search, the model only summarizes
// the snippets (no tool calling, so it works with any model), and the source
// list is appended by code. Falls back to a plain result list without LLM.
func (a *Assistant) Search(ctx context.Context, s search.Searcher, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "Pakai: /cari <kueri>, mis. /cari jadwal libur nasional 2027"
	}
	if utf8.RuneCountInString(query) > 300 {
		return "Kuerinya terlalu panjang (maks 300 karakter)."
	}
	rs, err := s.Search(ctx, query)
	switch {
	case errors.Is(err, search.ErrDisabled):
		return "Pencarian web belum disetel (TAVILY_API_KEY kosong)."
	case errors.Is(err, search.ErrQuota):
		return "Kuota pencarian web bulan ini habis."
	case err != nil:
		a.Log.Warn("web search failed", "err", err)
		return "Maaf, pencarian web sedang gagal. Coba lagi nanti."
	case len(rs) == 0:
		return "Tidak ada hasil untuk: " + query
	}
	if len(rs) > 5 {
		rs = rs[:5]
	}
	var facts, sources strings.Builder
	for i, r := range rs {
		snip := r.Content
		if rr := []rune(snip); len(rr) > maxSnippet {
			snip = string(rr[:maxSnippet]) + "…"
		}
		fmt.Fprintf(&facts, "[%d] %s\n%s\n\n", i+1, r.Title, snip)
		fmt.Fprintf(&sources, "\n[%d] %s", i+1, r.URL)
	}
	answer := a.phrase(ctx, a.ActiveForm(ctx),
		"Jawab pertanyaan berikut dengan singkat (maks 4 kalimat) HANYA berdasarkan hasil pencarian di FAKTA. "+
			"Sebut nomor sumber seperti [1]. Bila hasil tidak menjawab, katakan tidak ditemukan. Pertanyaan: "+query,
		facts.String())
	if answer == facts.String() { // LLM unavailable: list titles only
		var b strings.Builder
		b.WriteString("Hasil pencarian:")
		for i, r := range rs {
			fmt.Fprintf(&b, "\n[%d] %s", i+1, r.Title)
		}
		answer = b.String()
	}
	return strings.TrimSpace(answer) + "\n\nSumber:" + sources.String()
}
