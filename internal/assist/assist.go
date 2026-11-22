// Package assist adds natural language on top of the bot: intent
// classification with confirmation, /tanya over fixed query templates, daily
// summaries, /form and persona chat. Facts (times, numbers) always come from
// code; the model only phrases them.
package assist

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/forms"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/reminders"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/modes"
)

// Settings keys.
const (
	FormOverrideKey = "form_override"
	SummaryTimeKey  = "daily_summary_time" // "HH:MM", default 21:00
	SummaryLastKey  = "daily_summary_last" // "YYYY-MM-DD"
)

// MaxInputLen bounds free text sent to the model (runes).
const MaxInputLen = 500

const pendingTTL = 10 * time.Minute

// Assistant implements the Phase 3 features.
type Assistant struct {
	DB    *store.DB
	LLM   llm.Completer
	Modes map[string]modes.Mode
	Loc   *time.Location
	Now   func() time.Time
	Log   *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingAction
}

type pendingAction struct {
	kind       string // reminder | task
	text       string
	at         time.Time
	recurrence string
	expires    time.Time
}

// New creates an assistant.
func New(db *store.DB, c llm.Completer, ms map[string]modes.Mode, loc *time.Location, now func() time.Time, log *slog.Logger) *Assistant {
	return &Assistant{DB: db, LLM: c, Modes: ms, Loc: loc, Now: now, Log: log, pending: map[string]pendingAction{}}
}

// Replies when the LLM cannot answer. The bot always says something, never stays silent.
const (
	msgLLMFailed   = "LLM sedang bermasalah, coba lagi."
	msgLLMCap      = "Jatah LLM hari ini habis. Sementara pakai perintah ya, ketik /help."
	msgLLMDisabled = "AI belum diaktifkan di server. Pakai perintah ya, ketik /help."
)

// LLMFailText is the user-facing reply for a Complete error.
func LLMFailText(err error) string {
	switch llm.Kind(err) {
	case llm.KindCap:
		return msgLLMCap
	case llm.KindDisabled:
		return msgLLMDisabled
	}
	return msgLLMFailed
}

// --- intent classification -------------------------------------------------

// Intent is the strictly validated classifier output.
type Intent struct {
	Intent   string `json:"intent"`             // reminder | task | question | chat
	When     string `json:"when,omitempty"`     // reminder: time in /remind grammar
	Text     string `json:"text,omitempty"`     // reminder text
	Title    string `json:"title,omitempty"`    // task title
	Template string `json:"template,omitempty"` // question: query template
	Range    string `json:"range,omitempty"`    // question: time range
}

func classifyPrompt(now string) string {
	return `Kamu pengklasifikasi maksud untuk asisten pribadi. Sekarang: ` + now + `.
Balas HANYA satu objek JSON tanpa teks lain, dengan salah satu bentuk:
{"intent":"reminder","when":"<waktu>","text":"<isi pengingat>"}
{"intent":"task","title":"<judul tugas>"}
{"intent":"question","template":"<templat>","range":"<rentang>"}
{"intent":"chat"}

"when" WAJIB memakai salah satu format ini persis:
"17:00" (hari ini/besok bila lewat), "besok 09:00", "lusa 09:00", "+30m", "+2j", "2026-12-01 09:00",
"tiap hari 07:00", "tiap hari kerja 07:00", "tiap senin 08:00". Jam 24 jam, "5 sore" = "17:00".
"question" hanya untuk pertanyaan tentang aktivitas/pemakaian aplikasi di laptop.
templat: top_apps | longest_session | total_active | by_category
rentang: today | yesterday | this_week | last_7_days | this_month
Selain itu (sapaan, obrolan, pertanyaan umum) pakai {"intent":"chat"}.`
}

// ParseIntent strictly validates model output.
func ParseIntent(raw string) (Intent, error) {
	js, ok := llm.ExtractJSON(raw)
	if !ok {
		return Intent{}, errors.New("no json object")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(js)))
	dec.DisallowUnknownFields()
	var in Intent
	if err := dec.Decode(&in); err != nil {
		return Intent{}, fmt.Errorf("invalid json: %w", err)
	}
	trim := func(s *string) { *s = strings.TrimSpace(*s) }
	trim(&in.When)
	trim(&in.Text)
	trim(&in.Title)
	switch in.Intent {
	case "reminder":
		if in.When == "" || in.Text == "" || in.Title != "" || in.Template != "" || in.Range != "" {
			return Intent{}, errors.New("reminder needs exactly when and text")
		}
		if utf8.RuneCountInString(in.Text) > reminders.MaxTextLen {
			return Intent{}, errors.New("text too long")
		}
	case "task":
		if in.Title == "" || in.When != "" || in.Text != "" || in.Template != "" || in.Range != "" {
			return Intent{}, errors.New("task needs exactly title")
		}
		if utf8.RuneCountInString(in.Title) > bot.MaxTitleLen {
			return Intent{}, errors.New("title too long")
		}
	case "question":
		if !validTemplates[in.Template] || !validRanges[in.Range] || in.When != "" || in.Text != "" || in.Title != "" {
			return Intent{}, errors.New("question needs a valid template and range")
		}
	case "chat":
		if in.When != "" || in.Text != "" || in.Title != "" || in.Template != "" || in.Range != "" {
			return Intent{}, errors.New("chat takes no fields")
		}
	default:
		return Intent{}, fmt.Errorf("unknown intent %q", in.Intent)
	}
	return in, nil
}

// HandleText handles a free-text message from the owner.
func (a *Assistant) HandleText(ctx context.Context, text string) bot.Reply {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > MaxInputLen {
		return bot.Reply{Text: fmt.Sprintf("Pesannya terlalu panjang (maks %d karakter).", MaxInputLen)}
	}
	now := a.Now()
	raw, err := a.LLM.Complete(ctx, []llm.Message{
		{Role: "system", Content: classifyPrompt(reminders.FormatDate(now, now, a.Loc) + " (" + now.In(a.Loc).Format("2006-01-02 15:04 MST") + ")")},
		{Role: "user", Content: text},
	})
	if err != nil {
		a.logLLMErr("classify", err)
		return bot.Reply{Text: LLMFailText(err)}
	}
	in, err := ParseIntent(raw)
	if err != nil {
		a.Log.Warn("intent rejected", "err", err)
		return bot.Reply{Text: "Maaf, aku kurang paham. Coba pakai /remind, /task, atau /tanya ya."}
	}
	switch in.Intent {
	case "reminder":
		return a.proposeReminder(in, now)
	case "task":
		return a.propose(pendingAction{kind: "task", text: in.Title}, "Tambah tugas: "+in.Title+"?")
	case "question":
		return bot.Reply{Text: a.answer(ctx, text, in.Template, in.Range)}
	}
	return bot.Reply{Text: a.Chat(ctx, text)}
}

func (a *Assistant) proposeReminder(in Intent, now time.Time) bot.Reply {
	// The time is computed and validated by code, never trusted from the model.
	p, err := reminders.Parse(in.When+" "+in.Text, now, a.Loc)
	if err != nil {
		return bot.Reply{Text: "Aku menangkap pengingat, tapi waktunya tidak valid (" + err.Error() + "). Coba pakai /remind."}
	}
	q := fmt.Sprintf("Buat pengingat %s: %s", reminders.FormatTime(p.At, now, a.Loc), p.Text)
	if d := reminders.DescribeRecurrence(p.Recurrence); d != "" {
		q += " (" + d + ")"
	}
	return a.propose(pendingAction{kind: "reminder", text: p.Text, at: p.At, recurrence: p.Recurrence}, q+"?")
}

func (a *Assistant) propose(p pendingAction, question string) bot.Reply {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	p.expires = a.Now().Add(pendingTTL)
	a.mu.Lock()
	for k, v := range a.pending { // drop expired
		if a.Now().After(v.expires) {
			delete(a.pending, k)
		}
	}
	a.pending[id] = p
	a.mu.Unlock()
	return bot.Reply{Text: question, Buttons: []bot.Button{{Text: "Ya", Data: "ok:" + id}, {Text: "Batal", Data: "no:" + id}}}
}

// HandleCallback resolves a Ya/Batal button. Returns the new message text.
func (a *Assistant) HandleCallback(ctx context.Context, data string) string {
	verb, id, _ := strings.Cut(data, ":")
	a.mu.Lock()
	p, ok := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if !ok || a.Now().After(p.expires) {
		return "Konfirmasi ini sudah kedaluwarsa. Kirim ulang permintaannya ya."
	}
	if verb != "ok" {
		return "Oke, dibatalkan."
	}
	now := a.Now()
	switch p.kind {
	case "reminder":
		if !p.at.After(now) {
			return "Waktunya sudah lewat. Kirim ulang dengan waktu baru ya."
		}
		r, err := a.DB.CreateReminder(ctx, p.text, p.at, p.recurrence)
		if err != nil {
			a.Log.Error("create reminder failed", "err", err)
			return "Maaf, gagal menyimpan pengingat."
		}
		msg := fmt.Sprintf("Oke, diingatkan %s: %s", reminders.FormatTime(r.RemindAt, now, a.Loc), r.Text)
		if d := reminders.DescribeRecurrence(r.Recurrence); d != "" {
			msg += " (" + d + ")"
		}
		return msg + fmt.Sprintf(" [#%d]", r.ID)
	case "task":
		t, err := a.DB.CreateTask(ctx, p.text)
		if err != nil {
			a.Log.Error("create task failed", "err", err)
			return "Maaf, gagal menyimpan tugas."
		}
		return fmt.Sprintf("✅ Tugas #%d dicatat: %s", t.ID, t.Title)
	}
	return "Oke."
}

// --- persona ----------------------------------------------------------------

// ActiveForm is the manual override, or biasa. (The server does not see the
// agent's computed form; see DECISIONS.)
func (a *Assistant) ActiveForm(ctx context.Context) forms.Form {
	var v string
	if ok, err := a.DB.GetSetting(ctx, FormOverrideKey, &v); err == nil && ok && forms.Valid(forms.Form(v)) {
		return forms.Form(v)
	}
	return forms.Biasa
}

func (a *Assistant) voice(f forms.Form) string {
	if m, ok := a.Modes[string(f)]; ok {
		return m.Voice
	}
	return a.Modes[string(forms.Biasa)].Voice
}

const factRule = "\nAturan: data (waktu, angka, nama aplikasi) di bagian FAKTA adalah kebenaran; jangan mengubah, menambah, atau mengarang data. Jangan meniru karakter dari film, game, atau anime mana pun."

// Chat replies in the active persona (no personal data is sent).
func (a *Assistant) Chat(ctx context.Context, text string) string {
	out, err := a.ChatAs(ctx, a.ActiveForm(ctx), text)
	if err != nil {
		return LLMFailText(err)
	}
	return out
}

// ChatAs replies in the given form's persona. The error (see llm.Kind) lets
// callers tell the user why there is no answer.
func (a *Assistant) ChatAs(ctx context.Context, f forms.Form, text string) (string, error) {
	out, err := a.LLM.Complete(ctx, []llm.Message{
		{Role: "system", Content: a.voice(f) + factRule},
		{Role: "user", Content: text},
	})
	if err != nil {
		a.logLLMErr("chat", err)
		return "", err
	}
	return out, nil
}

// phrase asks the persona to write from facts; falls back to the facts text.
func (a *Assistant) phrase(ctx context.Context, f forms.Form, instruction, facts string) string {
	out, err := a.LLM.Complete(ctx, []llm.Message{
		{Role: "system", Content: a.voice(f) + factRule},
		{Role: "user", Content: instruction + "\n\nFAKTA:\n" + facts},
	})
	if err != nil {
		a.logLLMErr("phrase", err)
		return facts
	}
	return out
}

// --- /form ------------------------------------------------------------------

// SetForm handles /form <id>|auto.
func (a *Assistant) SetForm(ctx context.Context, arg string) (string, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		cur := a.ActiveForm(ctx)
		return fmt.Sprintf("Form sekarang: %s. Pakai: /form <%s> atau /form auto", cur, joinForms()), nil
	}
	if arg == "auto" {
		if err := a.DB.DeleteSetting(ctx, FormOverrideKey); err != nil {
			return "", err
		}
		if err := a.DB.CreateEvent(ctx, "form.set", map[string]string{"form": "auto"}); err != nil {
			return "", err
		}
		return "Form kembali otomatis.", nil
	}
	if !forms.Valid(forms.Form(arg)) {
		return "Form tidak dikenal. Pilihan: " + joinForms() + ", atau auto.", nil
	}
	if err := a.DB.SetSetting(ctx, FormOverrideKey, arg); err != nil {
		return "", err
	}
	if err := a.DB.CreateEvent(ctx, "form.set", map[string]string{"form": arg}); err != nil {
		return "", err
	}
	name := arg
	if m, ok := a.Modes[arg]; ok {
		name = m.Name
	}
	return "Form diatur ke " + name + ".", nil
}

func joinForms() string {
	s := make([]string, len(forms.All))
	for i, f := range forms.All {
		s[i] = string(f)
	}
	return strings.Join(s, ", ")
}

func (a *Assistant) logLLMErr(op string, err error) {
	if errors.Is(err, llm.ErrDisabled) || errors.Is(err, llm.ErrCap) {
		a.Log.Info("llm unavailable", "op", op, "reason", err.Error())
		return
	}
	a.Log.Warn("llm failed", "op", op, "kind", llm.Kind(err), "err", err)
}
