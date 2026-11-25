package worklog

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/projreport"
)

// TelegramLimit is the longest message; a longer recap goes as a document.
const TelegramLimit = 4096

// KeyAIConsent records that the owner accepted sending worklog text to the
// model provider (asked once).
const KeyAIConsent = "worklog.ai_consent"

// Rekap builds the monthly recap without any LLM (PRD 6.9): working days,
// filled, missed (with dates), then each day's entries.
func (s *Service) Rekap(ctx context.Context, month time.Time) (string, error) {
	from, to := monthBounds(month)
	counts, err := s.DB.WorklogCounts(ctx, from, to)
	if err != nil {
		return "", err
	}
	rules, err := s.Rules(ctx, from, to)
	if err != nil {
		return "", err
	}
	today := s.today()
	if !today.Before(to) {
		today = to // a past month: every day counts
	}
	v := BuildMonth(from, today, counts, rules)
	es, err := s.DB.WorklogMonth(ctx, from)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Rekap worklog %s\n", MonthTitle(from))
	fmt.Fprintf(&b, "Hari kerja: %d · terisi: %d · terlewat: %d\n", v.Workdays, v.Filled, len(v.Missing))
	if len(v.Missing) > 0 {
		var ds []string
		for _, d := range v.Missing {
			ds = append(ds, fmt.Sprintf("%d", d.Day()))
		}
		fmt.Fprintf(&b, "Terlewat: %s %s\n", strings.Join(ds, ", "), monthNames[from.Month()-1][:3])
	}
	// per day in date order (the file keeps write order; a recap reads better by date)
	byDay := map[string][]string{}
	var days []time.Time
	for _, e := range es {
		k := key(e.Date)
		if _, ok := byDay[k]; !ok {
			days = append(days, e.Date)
		}
		byDay[k] = append(byDay[k], e.Text)
	}
	sortDates(days)
	for _, d := range days {
		fmt.Fprintf(&b, "\n%s\n", DayTitle(d))
		for _, t := range byDay[key(d)] {
			b.WriteString("• " + t + "\n")
		}
	}
	if len(days) == 0 {
		b.WriteString("\nBelum ada catatan bulan ini.\n")
	}
	pf := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, s.Loc)
	if ps, err := s.DB.ProjectTotalsBetween(ctx, pf, pf.AddDate(0, 1, 0)); err == nil {
		if sec := projreport.Section(ps, 8); sec != "" {
			b.WriteString("\n" + sec + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func sortDates(ds []time.Time) {
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j].Before(ds[j-1]); j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
}

// cmdRekap is /rekap [YYYY-MM] [ai].
func (s *Service) cmdRekap(ctx context.Context, args string) bot.Reply {
	month := s.today()
	ai := false
	for _, f := range strings.Fields(strings.ToLower(args)) {
		switch {
		case f == "ai":
			ai = true
		default:
			t, err := time.Parse("2006-01", f)
			if err != nil {
				return bot.Reply{Text: "Pakai: /rekap [YYYY-MM] [ai]"}
			}
			month = t
		}
	}
	if ai {
		return s.askAI(ctx, month)
	}
	return s.sendRekap(ctx, month)
}

// sendRekap replies with the recap, or sends it as a document when it is
// longer than Telegram allows.
func (s *Service) sendRekap(ctx context.Context, month time.Time) bot.Reply {
	text, err := s.Rekap(ctx, month)
	if err != nil {
		return errReply(s, "rekap", err)
	}
	if utf8.RuneCountInString(text) <= TelegramLimit {
		return bot.Reply{Text: text}
	}
	name := "rekap-" + month.Format("2006-01") + ".md"
	if s.SendDoc == nil {
		return bot.Reply{Text: "Rekapnya terlalu panjang untuk satu pesan."}
	}
	if err := s.SendDoc(ctx, name, []byte(text+"\n"), "📊 Rekap worklog "+MonthTitle(month)); err != nil {
		return errReply(s, "rekap doc", err)
	}
	return bot.Reply{Text: "📄 Rekapnya panjang, kukirim sebagai dokumen " + name + "."}
}

func (s *Service) askAI(ctx context.Context, month time.Time) bot.Reply {
	if s.LLM == nil {
		return bot.Reply{Text: "AI belum diaktifkan di server. Pakai /rekap tanpa ai ya."}
	}
	var ok bool
	if _, err := s.DB.GetSetting(ctx, KeyAIConsent, &ok); err != nil {
		return errReply(s, "settings", err)
	}
	if !ok {
		return bot.Reply{
			Text: "⚠️ Rekap AI mengirim teks worklog bulan ini ke penyedia model (lewat Bifrost). Isinya bisa memuat info kantor. Lanjut?",
			Rows: [][]bot.Button{{
				{Text: "✅ Lanjut", Data: "rai:" + month.Format("2006-01")},
				{Text: "Batal", Data: "rxn"},
			}},
		}
	}
	return s.aiRekap(ctx, month)
}

// maxAIInput bounds the worklog text sent to the model (runes).
const maxAIInput = 8000

func (s *Service) aiRekap(ctx context.Context, month time.Time) bot.Reply {
	text, err := s.Rekap(ctx, month)
	if err != nil {
		return errReply(s, "rekap", err)
	}
	if rs := []rune(text); len(rs) > maxAIInput {
		text = string(rs[:maxAIInput])
	}
	out, err := s.LLM.Complete(ctx, []llm.Message{
		{Role: "system", Content: "Kamu merangkum catatan kerja bulanan seseorang. Tulis 5-8 poin singkat dalam Bahasa Indonesia: " +
			"pekerjaan utama, pola, dan hal yang menonjol. Hanya berdasarkan catatan yang diberikan; jangan menambah fakta, " +
			"angka, atau nama yang tidak ada."},
		{Role: "user", Content: text},
	})
	if err != nil {
		s.Log.Warn("rekap ai failed", "kind", llm.Kind(err), "err", err)
		return bot.Reply{Text: "AI sedang bermasalah, jadi ini rekap biasa:\n\n" + text}
	}
	return bot.Reply{Text: "🤖 Rekap AI " + MonthTitle(month) + "\n\n" + out}
}

// rekapCallback handles rek:/rai:/rxn.
func (s *Service) rekapCallback(ctx context.Context, verb, arg string) (bot.Reply, bool) {
	switch verb {
	case "rxn":
		return bot.Reply{Text: "Oke, rekap AI dibatalkan."}, true
	case "rek", "rai":
		month, err := time.Parse("2006-01", arg)
		if err != nil {
			return bot.Reply{Keep: true}, true
		}
		var r bot.Reply
		if verb == "rai" {
			if err := s.DB.SetSetting(ctx, KeyAIConsent, true); err != nil {
				return errReply(s, "settings", err), true
			}
			r = s.aiRekap(ctx, month)
		} else {
			r = s.sendRekap(ctx, month)
		}
		if verb == "rek" && s.Send != nil { // from the calendar: keep the calendar, send the recap below it
			if err := s.Send(ctx, r); err != nil {
				return errReply(s, "send", err), true
			}
			return bot.Reply{Keep: true, Toast: "Rekap dikirim"}, true
		}
		return r, true
	}
	return bot.Reply{}, false
}
