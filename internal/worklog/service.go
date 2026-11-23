package worklog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Pending kinds (bot_pending.kind).
const (
	PendingWrite = "worklog.write" // after "Tulis sekarang" on a reminder (Phase 7b)
	PendingAdd   = "worklog.add"
	PendingEdit  = "worklog.edit"
	pendingTTL   = 30 * time.Minute
)

// Service is the Telegram side of the worklog: /log, /cal, /libur, /kerja,
// the calendar buttons and text replies after a button.
type Service struct {
	DB     *store.DB
	Loc    *time.Location
	Now    func() time.Time
	Log    *slog.Logger
	ChatID int64 // the owner's private chat (bot_pending key)
	// OnSaved is called after an entry is saved (closes open reminders).
	OnSaved func(ctx context.Context, e store.WorklogEntry, via string, promptID int64)
	// Extra handles later commands (/worklog) and callbacks.
	ExtraCommand  func(ctx context.Context, cmd, args string) (bot.Reply, bool)
	ExtraCallback func(ctx context.Context, data string) (bot.Reply, bool)
	// Send posts a new message; SendDoc a file (long recaps). LLM is only
	// used by /rekap ai. All optional.
	Send    func(ctx context.Context, r bot.Reply) error
	SendDoc func(ctx context.Context, name string, data []byte, caption string) error
	LLM     llm.Completer
}

type pendingPayload struct {
	Date     string `json:"date,omitempty"`
	ID       int64  `json:"id,omitempty"`
	PromptID int64  `json:"prompt_id,omitempty"`
}

func (s *Service) today() time.Time { return LocalDate(s.Now(), s.Loc) }

// Rules loads working-day rules for [from, to).
func (s *Service) Rules(ctx context.Context, from, to time.Time) (Rules, error) {
	r := Rules{Holidays: map[string][]HolidayInfo{}}
	hs, err := s.DB.HolidaysBetween(ctx, from, to)
	if err != nil {
		return r, err
	}
	for _, h := range hs {
		k := key(h.Day)
		r.Holidays[k] = append(r.Holidays[k], HolidayInfo{Name: h.Name, Type: h.Type})
	}
	r.Overrides, err = s.DB.Overrides(ctx, from, to)
	return r, err
}

func monthBounds(m time.Time) (time.Time, time.Time) {
	from := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 1, 0)
}

func errReply(s *Service, op string, err error) bot.Reply {
	s.Log.Error("worklog failed", "op", op, "err", err)
	return bot.Reply{Text: "Maaf, ada gangguan saat membaca/menyimpan worklog. Coba lagi sebentar."}
}

// Command handles /log, /cal, /libur, /kerja (and Extra).
func (s *Service) Command(ctx context.Context, cmd, args string) (bot.Reply, bool) {
	switch cmd {
	case "log":
		return s.cmdLog(ctx, args), true
	case "cal", "kalender":
		m := s.today()
		if args != "" {
			t, err := time.Parse("2006-01", strings.TrimSpace(args))
			if err != nil {
				return bot.Reply{Text: "Pakai: /cal atau /cal YYYY-MM"}, true
			}
			m = t
		}
		return s.monthReply(ctx, m), true
	case "rekap":
		return s.cmdRekap(ctx, args), true
	case "libur":
		return s.cmdOverride(ctx, "off", args), true
	case "kerja":
		return s.cmdOverride(ctx, "work", args), true
	}
	if s.ExtraCommand != nil {
		return s.ExtraCommand(ctx, cmd, args)
	}
	return bot.Reply{}, false
}

const logUsage = "Pakai: /log [kemarin|YYYY-MM-DD] <teks>\nContoh: /log rapat sprint dan review PR\n/log kemarin perbaiki bug login"

// ValidateText normalizes an entry and checks its length.
func ValidateText(raw string) (string, error) {
	t := NormalizeText(raw)
	switch n := utf8.RuneCountInString(t); {
	case n == 0:
		return "", errors.New("teks catatan kosong")
	case n > MaxTextLen:
		return "", fmt.Errorf("catatan terlalu panjang (%d karakter, maks %d)", n, MaxTextLen)
	}
	return t, nil
}

func (s *Service) cmdLog(ctx context.Context, args string) bot.Reply {
	d, rest, err := ParseDateArg(args, s.Now(), s.Loc)
	if err != nil {
		return bot.Reply{Text: "❌ " + err.Error() + ". Worklog hanya untuk hari ini atau yang sudah lewat."}
	}
	if strings.TrimSpace(rest) == "" {
		return bot.Reply{Text: logUsage}
	}
	text, err := ValidateText(rest)
	if err != nil {
		return bot.Reply{Text: "❌ " + err.Error() + "."}
	}
	return s.Save(ctx, d, text, "telegram", 0)
}

// Save stores an entry and returns the confirmation with Undo / Add more.
func (s *Service) Save(ctx context.Context, d time.Time, text, source string, promptID int64) bot.Reply {
	e, n, err := s.DB.AddWorklog(ctx, d, text, source)
	if err != nil {
		return errReply(s, "add", err)
	}
	if s.OnSaved != nil {
		s.OnSaved(ctx, e, source, promptID)
	}
	when := "hari ini"
	if !d.Equal(s.today()) {
		when = DayTitle(d)
	}
	return bot.Reply{
		Text: fmt.Sprintf("✅ Tersimpan (%d catatan %s)", n, when),
		Rows: [][]bot.Button{{
			{Text: "↩️ Batalkan", Data: fmt.Sprintf("und:%d", e.ID)},
			{Text: "➕ Tambah lagi", Data: cbAdd(d)},
		}, {
			{Text: "📅 Lihat harinya", Data: cbDay(d)},
		}},
	}
}

func (s *Service) cmdOverride(ctx context.Context, kind, args string) bot.Reply {
	cmd := map[string]string{"off": "/libur", "work": "/kerja"}[kind]
	f := strings.Fields(args)
	remove := len(f) > 0 && strings.EqualFold(f[0], "hapus")
	if remove {
		f = f[1:]
	}
	d := s.today()
	if len(f) > 0 {
		t, err := time.Parse(time.DateOnly, f[0])
		if err == nil {
			d, f = t, f[1:]
		}
	}
	if remove {
		ok, err := s.DB.DeleteOverride(ctx, d)
		if err != nil {
			return errReply(s, "override", err)
		}
		if !ok {
			return bot.Reply{Text: fmt.Sprintf("%s tidak punya tanda manual.", DayTitle(d))}
		}
		return bot.Reply{Text: fmt.Sprintf("Tanda manual %s dihapus; kembali mengikuti kalender.", DayTitle(d))}
	}
	note := strings.Join(f, " ")
	if utf8.RuneCountInString(note) > 200 {
		return bot.Reply{Text: "Catatannya terlalu panjang (maks 200 karakter)."}
	}
	if err := s.DB.SetOverride(ctx, d, kind, note); err != nil {
		return errReply(s, "override", err)
	}
	if kind == "off" {
		return bot.Reply{Text: fmt.Sprintf("🔴 %s ditandai libur: tidak ada pengingat worklog. (Batalkan: %s hapus %s)", DayTitle(d), cmd, key(d))}
	}
	return bot.Reply{Text: fmt.Sprintf("💼 %s ditandai hari kerja: pengingat worklog tetap jalan. (Batalkan: %s hapus %s)", DayTitle(d), cmd, key(d))}
}

func (s *Service) monthReply(ctx context.Context, m time.Time) bot.Reply {
	from, to := monthBounds(m)
	counts, err := s.DB.WorklogCounts(ctx, from, to)
	if err != nil {
		return errReply(s, "counts", err)
	}
	rules, err := s.Rules(ctx, from, to)
	if err != nil {
		return errReply(s, "rules", err)
	}
	v := BuildMonth(m, s.today(), counts, rules)
	return bot.Reply{Text: v.Text(), Rows: v.Keyboard()}
}

func (s *Service) dayReply(ctx context.Context, d time.Time) bot.Reply {
	es, err := s.DB.WorklogByDate(ctx, d)
	if err != nil {
		return errReply(s, "day", err)
	}
	rules, err := s.Rules(ctx, d, d.AddDate(0, 0, 1))
	if err != nil {
		return errReply(s, "rules", err)
	}
	refs := make([]EntryRef, len(es))
	for i, e := range es {
		refs[i] = EntryRef{ID: e.ID, Text: e.Text}
	}
	return bot.Reply{Text: DayText(d, refs, rules), Rows: DayKeyboard(d, refs)}
}

// Callback handles calendar buttons (owner-only is checked by the bot).
func (s *Service) Callback(ctx context.Context, data string) (bot.Reply, bool) {
	verb, arg, _ := strings.Cut(data, ":")
	switch verb {
	case CbNoop:
		return bot.Reply{Keep: true}, true
	case "cal":
		m := s.today()
		if arg != "now" {
			t, err := time.Parse("2006-01", arg)
			if err != nil {
				return bot.Reply{Keep: true}, true
			}
			m = t
		}
		return s.monthReply(ctx, m), true
	case "day":
		d, err := time.Parse(time.DateOnly, arg)
		if err != nil {
			return bot.Reply{Keep: true}, true
		}
		return s.dayReply(ctx, d), true
	case "add":
		d, err := time.Parse(time.DateOnly, arg)
		if err != nil || d.After(s.today()) {
			return bot.Reply{Keep: true, Toast: "Tanggal itu belum terjadi."}, true
		}
		if err := s.DB.SetPending(ctx, s.ChatID, PendingAdd, pendingPayload{Date: key(d)}, s.Now().Add(pendingTTL)); err != nil {
			return errReply(s, "pending", err), true
		}
		return bot.Reply{Text: "✍️ Kirim teks catatan untuk " + DayTitle(d) + ".\n(Satu pesan = satu catatan; berlaku 30 menit.)",
			Rows: [][]bot.Button{{{Text: "❌ Batal", Data: CbCancelWait + ":" + key(d)}}}}, true
	case CbCancelWait:
		if err := s.DB.ClearPending(ctx, s.ChatID); err != nil {
			return errReply(s, "pending", err), true
		}
		if d, err := time.Parse(time.DateOnly, arg); err == nil {
			return s.dayReply(ctx, d), true
		}
		return bot.Reply{Text: "Oke, dibatalkan."}, true
	case "rek", "rai", "rxn":
		return s.rekapCallback(ctx, verb, arg)
	case "edt", "del", "dly", "und":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return bot.Reply{Keep: true}, true
		}
		return s.entryAction(ctx, verb, id), true
	}
	if s.ExtraCallback != nil {
		return s.ExtraCallback(ctx, data)
	}
	return bot.Reply{}, false
}

func (s *Service) entryAction(ctx context.Context, verb string, id int64) bot.Reply {
	e, err := s.DB.WorklogEntry(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return bot.Reply{Text: "Catatan itu sudah tidak ada.", Rows: [][]bot.Button{{{Text: "📅 Kalender", Data: "cal:now"}}}}
	}
	if err != nil {
		return errReply(s, "entry", err)
	}
	switch verb {
	case "edt":
		if err := s.DB.SetPending(ctx, s.ChatID, PendingEdit, pendingPayload{ID: id}, s.Now().Add(pendingTTL)); err != nil {
			return errReply(s, "pending", err)
		}
		return bot.Reply{Text: "✏️ Kirim teks pengganti untuk catatan ini:\n" + e.Text,
			Rows: [][]bot.Button{{{Text: "❌ Batal", Data: CbCancelWait + ":" + key(e.Date)}}}}
	case "del":
		return bot.Reply{Text: "🗑 Hapus catatan ini?\n" + e.Text, Rows: [][]bot.Button{{
			{Text: "🗑 Ya, hapus", Data: cbDeleteYes(id)},
			{Text: "Batal", Data: cbDay(e.Date)},
		}}}
	case "dly":
		if _, err := s.DB.DeleteWorklog(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return errReply(s, "delete", err)
		}
		r := s.dayReply(ctx, e.Date)
		r.Toast = "Dihapus"
		return r
	default: // und: undo right after saving
		if _, err := s.DB.DeleteWorklog(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return errReply(s, "undo", err)
		}
		return bot.Reply{Text: "↩️ Dibatalkan, catatan tadi dihapus.", Rows: [][]bot.Button{{{Text: "📅 Lihat harinya", Data: cbDay(e.Date)}}}}
	}
}

// PendingText takes a text message that answers a button (Tulis/Tambah/
// Edit). ok=false when nothing was waiting, so the text goes on to the
// natural-language pipeline.
func (s *Service) PendingText(ctx context.Context, text string) (bot.Reply, bool) {
	kind, raw, ok, err := s.DB.TakePending(ctx, s.ChatID, s.Now())
	if err != nil {
		return errReply(s, "pending", err), true
	}
	if !ok || !strings.HasPrefix(kind, "worklog.") {
		return bot.Reply{}, false
	}
	var p pendingPayload
	_ = json.Unmarshal(raw, &p)
	t, verr := ValidateText(text)
	if verr != nil {
		// keep waiting so the owner can just send a shorter text
		_ = s.DB.SetPending(ctx, s.ChatID, kind, p, s.Now().Add(pendingTTL))
		return bot.Reply{Text: "❌ " + verr.Error() + ". Kirim lagi ya."}, true
	}
	switch kind {
	case PendingEdit:
		e, err := s.DB.UpdateWorklog(ctx, p.ID, t, s.Now())
		if errors.Is(err, store.ErrNotFound) {
			return bot.Reply{Text: "Catatan itu sudah tidak ada."}, true
		}
		if err != nil {
			return errReply(s, "edit", err), true
		}
		return bot.Reply{Text: "✏️ Diubah.", Rows: [][]bot.Button{{{Text: "📅 Lihat harinya", Data: cbDay(e.Date)}}}}, true
	case PendingAdd:
		d, err := time.Parse(time.DateOnly, p.Date)
		if err != nil {
			d = s.today()
		}
		return s.Save(ctx, d, t, "telegram", 0), true
	default: // PendingWrite: today, closes the reminder that asked
		return s.Save(ctx, s.today(), t, "telegram", p.PromptID), true
	}
}
