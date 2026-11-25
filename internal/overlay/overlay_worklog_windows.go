//go:build windows

package overlay

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/logbatch"
)

// Worklog bubble and local questions on the overlay window (UI thread
// unless noted).

type worklogResult struct {
	res agent.WriteResult
	err error
}

// SetTray passes the tray/worklog options (call before Run).
func (o *Overlay) SetTray(t TrayOptions) {
	o.tray = t
	o.paused = t.State.TrackingPaused
	o.charHidden = !t.State.CharacterVisible
}

// WorklogEvent passes worklog.due / worklog.closed to the UI thread (safe
// from any goroutine). The agent has already checked idle/tidur/fokus.
func (o *Overlay) WorklogEvent(e agent.WorklogEvent) {
	select {
	case o.wlCh <- e:
	default:
	}
}

// SetWorklogQueue updates the tray's "waiting to sync" count (any goroutine).
func (o *Overlay) SetWorklogQueue(n int) { o.wlQueue.Store(int32(n)) }

func (o *Overlay) drainWorklog(now time.Time) {
	for {
		select {
		case e := <-o.wlCh:
			switch {
			case e.Type == "worklog.closed":
				o.p.WorklogClosed(e.PromptID, now)
			case !o.tray.State.WorklogBubbles || o.charHidden || o.CharacterOff:
				o.log.Debug("worklog bubble off", "prompt", e.PromptID)
			default:
				if o.p.WorklogDue(e.PromptID, now) {
					o.log.Info("worklog bubble shown", "prompt", e.PromptID)
				}
			}
		case r := <-o.wlResCh:
			if r.err != nil {
				o.log.Warn("worklog not saved", "err", r.err)
				o.p.WorklogSaved(false, "gagal menyimpan di laptop", now)
				continue
			}
			o.p.WorklogSaved(r.res.Synced, r.res.Reject, now)
		default:
			return
		}
	}
}

// worklogChar handles keys for the worklog states; handled=false lets the
// normal chat handling run.
func (o *Overlay) worklogChar(u uint16, now time.Time) bool {
	switch {
	case o.p.State == WorklogAsk:
		action, id := o.p.WorklogKey(rune(u), now)
		if action != WorklogNone && o.tray.Host != nil {
			verb := map[WorklogAction]string{WorklogSkip: "skip", WorklogMute: "mute"}[action]
			go func() {
				ctx, cancel := context.WithTimeout(o.ctx, 15*time.Second)
				defer cancel()
				if err := o.tray.Host.AnswerPrompt(ctx, id, verb); err != nil {
					o.log.Info("worklog answer not sent (Telegram still has the reminder)", "err", err)
				}
			}()
		}
		return true
	case u == 0x0D && (o.p.State == WorklogInput || (o.p.State == Input && IsLogCommand(o.p.Draft))):
		text, id := o.p.SubmitWorklog(now)
		if text != "" {
			// the same rules as the server: a bad paste is explained here, not saved partly
			b, err := logbatch.Parse(text, localToday(), localToday())
			if err != nil {
				o.p.Note("Gagal: "+err.Error(), now)
				return true
			}
			o.p.wlCount = len(b.Entries)
		}
		if text != "" && o.tray.Host != nil {
			go func() {
				ctx, cancel := context.WithTimeout(o.ctx, 20*time.Second)
				defer cancel()
				res, err := o.tray.Host.WriteWorklog(ctx, text, id)
				select {
				case o.wlResCh <- worklogResult{res, err}:
				case <-o.ctx.Done():
				}
			}()
		}
		return true
	}
	return false
}

// localConfirm answers a question asked by the agent itself (no server).
func (o *Overlay) localConfirm(yes bool, now time.Time) {
	id := o.p.confirmID
	switch id {
	case localAutostart:
		text := "Oke, nanti saja. Bisa dinyalakan dari menu tray."
		if yes {
			if err := agent.SetAutostart(o.tray.Registry, o.tray.ExePath, true); err != nil {
				o.log.Warn("autostart not set", "err", err)
				text = "Maaf, gagal menyalakan autostart."
			} else {
				text = "Oke, aku akan jalan otomatis saat laptop nyala."
			}
		}
		o.p.Note(text, now)
	case localClipboard:
		if !yes {
			o.p.Note(TextCancelled, now)
			return
		}
		o.p.BeginWorklogSave(o.clipCount, now)
		if o.tray.Host != nil {
			text := o.clipText
			go func() {
				ctx, cancel := context.WithTimeout(o.ctx, 20*time.Second)
				defer cancel()
				res, err := o.tray.Host.WriteWorklog(ctx, text, 0)
				select {
				case o.wlResCh <- worklogResult{res, err}:
				case <-o.ctx.Done():
				}
			}()
		}
	}
}

const (
	localAutostart = "local:autostart"
	localClipboard = "local:clipboard"
)

// localToday is the laptop's calendar date as midnight UTC (what logbatch compares).
func localToday() time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// clipboardWorklog is the tray's "Tulis worklog dari clipboard": read the
// text, split it with the /log rules and ask Enter (save) or Esc (cancel).
func (o *Overlay) clipboardWorklog(now time.Time) {
	text, err := readClipboard()
	if err != nil || strings.TrimSpace(text) == "" {
		o.p.Note("Clipboard-nya kosong (tidak ada teks).", now)
		o.sync(now)
		return
	}
	b, err := logbatch.Parse(text, localToday(), localToday())
	if err != nil {
		o.p.Note("Gagal: "+err.Error(), now)
		o.sync(now)
		return
	}
	o.clipText, o.clipCount = text, len(b.Entries)
	q := "Simpan " + b.Preview(dayLabel) + "?"
	if len(b.Entries) == 1 {
		q = "Simpan 1 catatan dari clipboard?"
	}
	if o.p.AskLocal(q, localClipboard, now) {
		o.sync(now)
		pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	}
}

func dayLabel(d time.Time) string {
	return fmt.Sprintf("%d %s", d.Day(), [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}[d.Month()-1])
}

// offerAutostart asks once on the first manual start.
func (o *Overlay) offerAutostart(now time.Time) {
	if !o.tray.OfferAutostart || o.tray.Registry == nil || o.CharacterOff {
		return
	}
	if on, _, err := agent.AutostartStatus(o.tray.Registry, o.tray.ExePath); err != nil || on {
		return
	}
	if o.p.AskLocal("Mau aku jalan otomatis saat laptop nyala?", localAutostart, now) {
		o.tray.State.AutostartOffered = true
		o.saveState()
		o.sync(now)
		pSetForegroundWindow.Call(o.hwnd) //nolint:errcheck
	}
}

func (o *Overlay) saveState() {
	o.tray.State.TrackingPaused = o.paused
	o.tray.State.CharacterVisible = !o.charHidden
	if o.tray.Host != nil {
		o.tray.Host.SaveState(o.tray.State)
	}
}

// isLocal reports whether the open question is the agent's own.
func (o *Overlay) isLocal() bool { return strings.HasPrefix(o.p.confirmID, "local:") }
