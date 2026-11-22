package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/reminders"
)

var validTemplates = map[string]bool{"top_apps": true, "longest_session": true, "total_active": true, "by_category": true}

var validRanges = map[string]bool{"today": true, "yesterday": true, "this_week": true, "last_7_days": true, "this_month": true}

var rangeLabels = map[string]string{
	"today": "hari ini", "yesterday": "kemarin", "this_week": "minggu ini",
	"last_7_days": "7 hari terakhir", "this_month": "bulan ini",
}

// Range resolves a named range to [from, to) in loc. Weeks start on Monday.
func Range(name string, now time.Time, loc *time.Location) (time.Time, time.Time) {
	dayStart, dayEnd := activity.DayRange(now, loc)
	switch name {
	case "yesterday":
		return dayStart.AddDate(0, 0, -1), dayStart
	case "this_week":
		wd := (int(dayStart.Weekday()) + 6) % 7 // Monday=0
		return dayStart.AddDate(0, 0, -wd), dayEnd
	case "last_7_days":
		return dayStart.AddDate(0, 0, -6), dayEnd
	case "this_month":
		return time.Date(dayStart.Year(), dayStart.Month(), 1, 0, 0, 0, 0, loc), dayEnd
	}
	return dayStart, dayEnd
}

const queryPrompt = `Pilih templat query untuk pertanyaan tentang aktivitas laptop pengguna.
Balas HANYA JSON: {"template":"<templat>","range":"<rentang>"}
templat: top_apps (aplikasi paling lama dipakai) | longest_session (sesi tanpa putus terpanjang) |
total_active (total waktu aktif) | by_category (per kategori: kerja/jelajah/hiburan/lainnya)
rentang: today | yesterday | this_week | last_7_days | this_month`

// ParseQuery strictly validates the template choice.
func ParseQuery(raw string) (template, rng string, err error) {
	js, ok := llm.ExtractJSON(raw)
	if !ok {
		return "", "", errors.New("no json object")
	}
	dec := json.NewDecoder(strings.NewReader(js))
	dec.DisallowUnknownFields()
	var q struct {
		Template string `json:"template"`
		Range    string `json:"range"`
	}
	if err := dec.Decode(&q); err != nil {
		return "", "", err
	}
	if !validTemplates[q.Template] || !validRanges[q.Range] {
		return "", "", errors.New("unknown template or range")
	}
	return q.Template, q.Range, nil
}

// Ask handles /tanya: the model picks a fixed template, code runs it, the
// model phrases the answer from the resulting facts. No text-to-SQL.
func (a *Assistant) Ask(ctx context.Context, question string) string {
	question = strings.TrimSpace(question)
	if question == "" {
		return "Pakai: /tanya <pertanyaan>, mis. /tanya minggu ini paling lama di aplikasi apa?"
	}
	if utf8.RuneCountInString(question) > MaxInputLen {
		return fmt.Sprintf("Pertanyaannya terlalu panjang (maks %d karakter).", MaxInputLen)
	}
	raw, err := a.LLM.Complete(ctx, []llm.Message{
		{Role: "system", Content: queryPrompt},
		{Role: "user", Content: question},
	})
	if err != nil {
		a.logLLMErr("query", err)
		return LLMFailText(err)
	}
	tpl, rng, err := ParseQuery(raw)
	if err != nil {
		a.Log.Warn("query choice rejected", "err", err)
		return "Maaf, aku belum bisa menjawab pertanyaan itu. Coba tanya soal aplikasi teratas, sesi terpanjang, total waktu aktif, atau per kategori."
	}
	return a.answer(ctx, question, tpl, rng)
}

func (a *Assistant) answer(ctx context.Context, question, tpl, rng string) string {
	facts, err := a.Facts(ctx, tpl, rng)
	if err != nil {
		a.Log.Error("query failed", "err", err)
		return "Maaf, ada gangguan saat membaca data."
	}
	return a.phrase(ctx, a.ActiveForm(ctx),
		"Jawab pertanyaan pengguna dengan singkat (maks 3 kalimat) hanya berdasarkan FAKTA. Pertanyaan: "+question, facts)
}

// Facts runs a fixed query template and renders aggregate facts as text.
// Only aggregates are included (never per-minute rows or window titles).
func (a *Assistant) Facts(ctx context.Context, tpl, rng string) (string, error) {
	now := a.Now()
	from, to := Range(rng, now, a.Loc)
	bs, err := a.DB.BucketsBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	s := activity.Summarize(bs)
	var b strings.Builder
	fmt.Fprintf(&b, "Rentang: %s (%s s.d. %s)\n", rangeLabels[rng],
		reminders.FormatDate(from, now, a.Loc), reminders.FormatDate(to.Add(-time.Second), now, a.Loc))
	if s.TotalActive == 0 {
		b.WriteString("Tidak ada data aktivitas pada rentang ini.")
		return b.String(), nil
	}
	switch tpl {
	case "top_apps":
		b.WriteString("Aplikasi teratas (waktu aktif):")
		for i, app := range s.Apps {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "\n%d. %s — %s", i+1, app.App, activity.FormatDuration(app.Active))
		}
	case "longest_session":
		if l := s.Longest; l != nil {
			fmt.Fprintf(&b, "Sesi terpanjang: %s selama %s, %s %s–%s", l.App, activity.FormatDuration(l.Duration()),
				reminders.FormatDate(l.Start, now, a.Loc), l.Start.In(a.Loc).Format("15:04"), l.End.In(a.Loc).Format("15:04"))
		}
	case "total_active":
		fmt.Fprintf(&b, "Total waktu aktif: %s", activity.FormatDuration(s.TotalActive))
	case "by_category":
		b.WriteString("Waktu aktif per kategori:")
		for _, c := range []string{"work", "browse", "distract", "other"} {
			if d := s.ByCategory[c]; d > 0 {
				fmt.Fprintf(&b, "\n• %s: %s", activity.CategoryLabel(c), activity.FormatDuration(d))
			}
		}
	}
	return b.String(), nil
}
