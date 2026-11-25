//go:build windows

package overlay

import (
	"context"
	"time"
	"unicode/utf16"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// Windows glue of the calendar card: keys in, requests out, results back on
// the UI thread. The model (calmodel.go) decides what a key means.

const (
	vkLeft   = 0x25
	vkRight  = 0x27
	vkDelete = 0x2E
)

// calResult is the outcome of one request, delivered to the UI thread.
type calResult struct {
	kind   CalReqKind
	month  agent.CalMonth
	day    agent.CalDayView
	stale  bool
	queued bool
	err    error
}

// openCalendar shows the card on today's month (tray "Kalender worklog…" or
// "/cal" in the bubble).
func (o *Overlay) openCalendar(now time.Time) {
	if o.tray.Host == nil || o.tray.Host.Calendar() == nil {
		o.p.Note("Kalender belum tersedia.", now)
		o.sync(now)
		return
	}
	o.p.Dismiss(now)
	o.cal = NewCalendar(localToday())
	o.calRun(CalReq{Kind: CalLoadMonth, Month: o.cal.MonthKey()})
	o.sync(now)
	pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
}

func (o *Overlay) closeCalendar(now time.Time) {
	o.cal = nil
	o.sync(now)
}

// calRun carries out a request off the UI thread.
func (o *Overlay) calRun(req CalReq) {
	src := o.tray.Host.Calendar()
	if src == nil || req.Kind == CalNone || req.Kind == CalClose {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(o.ctx, 15*time.Second)
		defer cancel()
		res := calResult{kind: req.Kind}
		switch req.Kind {
		case CalLoadMonth:
			res.month, res.stale, res.err = src.Month(ctx, req.Month)
		case CalLoadDay:
			res.day, res.stale, res.err = src.Day(ctx, req.Date)
		case CalAdd:
			var w agent.WriteResult
			w, res.err = src.AddOn(ctx, req.Date, req.Text)
			res.queued = w.Queued
			if w.Reject != "" {
				res.err = rejected(w.Reject)
			}
		case CalEdit:
			res.err = src.Edit(ctx, req.ID, req.Text)
		case CalDelete:
			res.err = src.Delete(ctx, req.ID)
		}
		select {
		case o.calCh <- res:
		case <-o.ctx.Done():
		}
	}()
}

type rejected string

func (r rejected) Error() string { return string(r) }

// drainCalendar applies finished requests to the card.
func (o *Overlay) drainCalendar(now time.Time) {
	for {
		select {
		case r := <-o.calCh:
			if o.cal == nil {
				continue
			}
			o.applyCalResult(r)
			o.sync(now)
		default:
			return
		}
	}
}

func (o *Overlay) applyCalResult(r calResult) {
	c := o.cal
	switch r.kind {
	case CalLoadMonth:
		if r.err != nil {
			c.Busy, c.Note = false, "Gagal memuat bulan"
			return
		}
		c.SetMonth(r.month, r.stale)
	case CalLoadDay:
		if r.err != nil {
			c.Busy, c.Note = false, "Gagal memuat hari"
			return
		}
		c.SetDay(r.day, r.stale)
	case CalAdd, CalEdit, CalDelete:
		c.Done(r.kind, r.err, r.queued)
		if r.err == nil {
			o.calRun(c.ReloadDay()) // the panel shows what the server has now
			o.calRun(CalReq{Kind: CalLoadMonth, Month: c.MonthKey()})
		}
	}
}

// calHandle runs what a key asked for.
func (o *Overlay) calHandle(req CalReq, now time.Time) {
	switch req.Kind {
	case CalClose:
		o.closeCalendar(now)
		return
	case CalNone:
	default:
		o.calRun(req)
	}
	o.sync(now)
}

func (o *Overlay) calChar(u uint16, now time.Time) {
	var req CalReq
	switch {
	case u == 0x16: // Ctrl+V
		if text, err := readClipboard(); err == nil {
			o.cal.Paste(text)
		}
	case u == 0x0D:
		req = o.cal.Key(KeyEnter)
	case u == 0x1B:
		req = o.cal.Key(KeyEsc)
	case u == 0x08:
		req = o.cal.Key(KeyBackspace)
	case utf16.IsSurrogate(rune(u)) && o.surrogate == 0:
		o.surrogate = u
		return
	case o.surrogate != 0:
		req = o.cal.Type([]rune{utf16.DecodeRune(rune(o.surrogate), rune(u))})
		o.surrogate = 0
	default:
		req = o.cal.Type([]rune{rune(u)})
	}
	o.calHandle(req, now)
}

func (o *Overlay) calKeyDown(vk uintptr, now time.Time) {
	keys := map[uintptr]CalKey{vkLeft: KeyLeft, vkRight: KeyRight, vkUp: KeyUp, vkDown: KeyDown,
		vkPrior: KeyPageUp, vkNext: KeyPageDown, vkDelete: KeyDelete}
	if k, ok := keys[vk]; ok {
		o.calHandle(o.cal.Key(k), now)
	}
}

// slash answers a slash command typed in the bubble. Commands never reach
// the LLM: status is answered here, the others by the server's whitelist.
func (o *Overlay) slash(ctx context.Context, req int, cmd, args string) {
	res := chatResult{req: req}
	h := o.tray.Host
	switch cmd {
	case "status":
		res.reply = h.StatusReport(ctx)
	case "log":
		res.reply = "Pakai: /log <teks>, bisa beberapa baris."
	case "cal":
		res.reply = "Ketik /cal saja, lalu Enter."
	default:
		r, err := h.Command(ctx, cmd, args)
		switch {
		case err != nil:
			res.fail = agent.ClassifyChatError(err)
			o.log.Warn("slash command failed", "cmd", cmd, "kind", string(res.fail))
		default:
			res.reply = agent.PlainText(r.Text)
		}
	}
	select {
	case o.replyCh <- res:
	case <-o.ctx.Done():
	}
}
