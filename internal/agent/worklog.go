package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// WorklogSaved is the server's answer to POST /v1/agent/worklog.
type WorklogSaved struct {
	ID        int64 `json:"id"`
	Count     int   `json:"count"`
	Entries   int   `json:"entries"`
	Duplicate bool  `json:"duplicate"`
}

// PostWorklog sends one entry (idempotent on ClientID).
func (c *Client) PostWorklog(ctx context.Context, it WorklogItem) (WorklogSaved, error) {
	var out WorklogSaved
	err := c.do(ctx, http.MethodPost, "/v1/agent/worklog", it, &out)
	return out, err
}

// MuteWorklog switches today's worklog reminders off (tray "Senyapkan hari ini").
func (c *Client) MuteWorklog(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/agent/worklog/mute", map[string]string{}, nil)
}

// AnswerPrompt answers a worklog reminder from the bubble ("skip": this slot
// only; "mute": no more reminders today).
func (c *Client) AnswerPrompt(ctx context.Context, id int64, action string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/v1/agent/worklog/prompts/%d/answer", id), map[string]string{"action": action}, nil)
}

// WorklogAPI is the part of the client the worklog queue needs.
type WorklogAPI interface {
	PostWorklog(ctx context.Context, it WorklogItem) (WorklogSaved, error)
	AnswerPrompt(ctx context.Context, id int64, action string) error
	MuteWorklog(ctx context.Context) error
}

// newClientID returns a random UUID (version 4).
func newClientID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// WriteResult is what the bubble shows after Enter.
type WriteResult struct {
	Synced bool   // the server confirmed: "Tersimpan ✓"
	Queued bool   // kept locally: "Tersimpan ✓ (menunggu sinkron)"
	Count  int    // entries on the first day of the paste (when synced)
	Saved  int    // entries stored by this paste (when synced)
	Reject string // the server refused it (e.g. too long): nothing kept
}

// WriteWorklog saves an entry from the laptop: first into the local queue
// (so it survives being offline), then it tries to send it. The queue keeps
// it until the server confirms; resends use the same client_id.
func (a *Agent) WriteWorklog(ctx context.Context, text string, promptID int64) (WriteResult, error) {
	a.init()
	return a.WriteWorklogOn(ctx, a.Now().Format(time.DateOnly), text, promptID)
}

// WriteWorklogOn is WriteWorklog for a given day ("YYYY-MM-DD"), used by the
// calendar's day panel. Several lines in text become several entries (the
// server splits them like /log).
func (a *Agent) WriteWorklogOn(ctx context.Context, date, text string, promptID int64) (WriteResult, error) {
	a.init()
	it := WorklogItem{ClientID: newClientID(), Date: date, Text: text, PromptID: promptID, CreatedAt: a.Now()}
	if err := a.Buf.QueueWorklog(ctx, it); err != nil {
		return WriteResult{}, err
	}
	a.queueChanged(ctx)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := a.sendWorklog(sctx, it)
	if err != nil {
		a.Log.Info("worklog queued until the server is reachable", "err", err)
		a.kickWorklog()
		return WriteResult{Queued: true}, nil
	}
	return res, nil
}

// sendWorklog posts one queued entry and removes it once the server has it
// (or has rejected it for good: a 400 will never succeed).
func (a *Agent) sendWorklog(ctx context.Context, it WorklogItem) (WriteResult, error) {
	api, ok := a.API.(WorklogAPI)
	if !ok {
		return WriteResult{}, errors.New("worklog not supported by this API")
	}
	saved, err := api.PostWorklog(ctx, it)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusBadRequest {
		a.Log.Warn("worklog entry rejected by the server", "reason", se.Reason)
		_ = a.Buf.DoneWorklog(ctx, it.ClientID)
		a.queueChanged(ctx)
		return WriteResult{Reject: se.Reason}, nil
	}
	if err != nil {
		return WriteResult{}, err
	}
	if err := a.Buf.DoneWorklog(ctx, it.ClientID); err != nil {
		return WriteResult{}, err
	}
	a.queueChanged(ctx)
	if saved.Duplicate {
		a.Log.Info("worklog resend acknowledged (already saved)")
	}
	return WriteResult{Synced: true, Count: saved.Count, Saved: saved.Entries}, nil
}

// FlushWorklog sends every queued entry, oldest first; it stops at the
// first failure (the rest wait for the next try). It returns how many
// were confirmed.
func (a *Agent) FlushWorklog(ctx context.Context) (int, error) {
	items, err := a.Buf.PendingWorklog(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		if _, err := a.sendWorklog(ctx, it); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// QueuedWorklog is the number of entries waiting to be sent (tray menu).
func (a *Agent) QueuedWorklog(ctx context.Context) int {
	items, err := a.Buf.PendingWorklog(ctx)
	if err != nil {
		return 0
	}
	return len(items)
}

func (a *Agent) queueChanged(ctx context.Context) {
	if cb := a.OnWorklogQueue; cb != nil {
		cb(a.QueuedWorklog(ctx))
	}
}

func (a *Agent) kickWorklog() {
	select {
	case a.worklogKick <- struct{}{}:
	default:
	}
}

// worklogLoop resends queued entries: right after a failed write, when the
// connection comes back, and otherwise with backoff (30 s doubling to 15 min).
func (a *Agent) worklogLoop(ctx context.Context) {
	failures := 0
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-a.worklogKick:
		}
		if a.QueuedWorklog(ctx) == 0 {
			failures, wait = 0, 5*time.Minute
			continue
		}
		n, err := a.FlushWorklog(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait = backoff(30*time.Second, failures)
			a.Log.Info("worklog resend failed, retrying later", "sent", n, "retry_in", wait.String(), "err", err)
			continue
		}
		if n > 0 {
			a.Log.Info("worklog queue sent", "entries", n)
		}
		failures, wait = 0, 5*time.Minute
	}
}
