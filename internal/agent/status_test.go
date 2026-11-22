package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/forms"
)

func reminderEvent(id int64, text string, created time.Time) Event {
	p, _ := json.Marshal(map[string]string{"text": text})
	return Event{ID: id, Type: "reminder.due", Payload: p, CreatedAt: created}
}

// Late reminders keep event order, are labelled with their delay, and very
// old ones are skipped; each is shown for the configured time in turn.
func TestLateRemindersInOrder(t *testing.T) {
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	var logBuf safeBuffer
	a := &Agent{Cfg: testCfg(), Log: slog.New(slog.NewTextHandler(&logBuf, nil)), Now: func() time.Time { return now },
		Sampler: SamplerFunc(func() (Sample, error) { return Sample{}, nil })}
	a.init()
	for _, e := range []Event{
		reminderEvent(1, "rapat", now.Add(-10*time.Second)),
		reminderEvent(2, "minum air", now.Add(-25*time.Minute)),
		reminderEvent(3, "kemarin", now.Add(-7*time.Hour)),
		reminderEvent(4, "telepon", now.Add(-185*time.Minute)),
		reminderEvent(5, "makan", now.Add(-2*time.Hour)),
	} {
		a.HandleEvent(e)
	}
	showFor := time.Duration(a.Cfg.Forms.ReminderDisplaySeconds) * time.Second
	var seen []string
	for range 6 {
		if err := a.SampleOnce(context.Background(), 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if f := a.CurrentForm(); f.Form == forms.Ingat {
			seen = append(seen, f.Speech)
		}
		now = now.Add(showFor)
	}
	want := []string{"rapat", "minum air (tertunda 25 menit)", "telepon (tertunda 3 jam 5 menit)", "makan (tertunda 2 jam)"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("shown %q\nwant  %q", seen, want)
	}
	log := logBuf.String()
	if !strings.Contains(log, "late reminder skipped") || !strings.Contains(log, "id=3") {
		t.Fatalf("skip not logged: %s", log)
	}
}

// scriptedAPI plays a fixed sequence of long-poll results.
type scriptedAPI struct {
	mu         sync.Mutex
	stateFails int // the next n State calls fail (server unreachable)
	stateCalls int
	polls      []func(after int64) ([]Event, error)
	afters     []int64
	acked      int64
	done       chan struct{}
}

func (s *scriptedAPI) PostActivity(context.Context, []Bucket) error { return nil }
func (s *scriptedAPI) State(context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateCalls++
	if s.stateFails > 0 {
		s.stateFails--
		return State{}, timeoutErr
	}
	return State{Now: time.Now()}, nil
}
func (s *scriptedAPI) Heartbeat(context.Context, string, bool) (int, error) {
	return 0, nil
}
func (s *scriptedAPI) Ack(_ context.Context, upTo int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acked = upTo
	return nil
}
func (s *scriptedAPI) Events(ctx context.Context, after int64, _ time.Duration) ([]Event, error) {
	s.mu.Lock()
	s.afters = append(s.afters, after)
	if len(s.polls) == 0 {
		s.mu.Unlock()
		select {
		case <-s.done:
		default:
			close(s.done)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	next := s.polls[0]
	s.polls = s.polls[1:]
	s.mu.Unlock()
	return next(after)
}

// timeoutErr looks like a long-poll request that timed out (laptop asleep,
// network gone).
var timeoutErr = &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}

func TestReconnectAfterLongPollTimeout(t *testing.T) {
	now := time.Now()
	api := &scriptedAPI{done: make(chan struct{})}
	api.polls = []func(int64) ([]Event, error){
		func(int64) ([]Event, error) { return []Event{reminderEvent(1, "awal", now)}, nil },
		func(int64) ([]Event, error) {
			// the long-poll times out (laptop asleep); reconnecting fails three more times
			api.stateFails = 3
			return nil, timeoutErr
		},
		func(int64) ([]Event, error) { // awake: everything that piled up, in order
			return []Event{
				reminderEvent(2, "pertama", now.Add(-40*time.Minute)),
				{ID: 3, Type: "task.created", Payload: []byte(`{}`), CreatedAt: now.Add(-30 * time.Minute)},
				reminderEvent(4, "kedua", now.Add(-20*time.Minute)),
			}, nil
		},
	}
	var logBuf safeBuffer
	var mu sync.Mutex
	var states []ConnState
	a := &Agent{Cfg: testCfg(), Buf: openBuf(t), API: api, RetryBase: time.Millisecond,
		Log: slog.New(slog.NewTextHandler(&logBuf, nil)),
		OnConn: func(s ConnState) {
			mu.Lock()
			states = append(states, s)
			mu.Unlock()
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.eventLoop(ctx)
	select {
	case <-api.done:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop stuck")
	}

	mu.Lock()
	got := states
	mu.Unlock()
	// starts as ConnConnecting (the zero value, so no callback): the tray starts yellow
	// 1 failed poll + 2 failed connects = yellow, the 3rd failure = red, then green.
	want := []ConnState{ConnOnline, ConnConnecting, ConnOffline, ConnOnline}
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states = %v, want %v", got, want)
		}
	}
	api.mu.Lock()
	afters, acked := api.afters, api.acked
	api.mu.Unlock()
	// every retry resumes after the last handled event: nothing lost or repeated
	if api.stateCalls != 5 {
		t.Fatalf("state calls = %d, want 5 (connect, 3 failed reconnects, reconnect)", api.stateCalls)
	}
	for i, want := range []int64{0, 1, 1, 4} {
		if afters[i] != want {
			t.Fatalf("poll %d after=%d, want %d (all: %v)", i, afters[i], want, afters)
		}
	}
	if acked != 4 {
		t.Fatalf("acked %d", acked)
	}
	log := logBuf.String()
	if !strings.Contains(log, "events connected") || !strings.Contains(log, "pending_events=3") ||
		!strings.Contains(log, "offline_for=") {
		t.Fatalf("reconnect not logged: %s", log)
	}
	a.mu.Lock()
	cur, queued := a.reminder, append([]string(nil), a.reminders...)
	a.mu.Unlock()
	if cur != "awal" || strings.Join(queued, "|") != "pertama (tertunda 40 menit)|kedua (tertunda 20 menit)" {
		t.Fatalf("reminders: current %q, queued %q", cur, queued)
	}
}

func TestConnAfter(t *testing.T) {
	for f, want := range map[int]ConnState{0: ConnOnline, 1: ConnConnecting, 2: ConnConnecting, 3: ConnOffline, 9: ConnOffline} {
		if connAfter(f) != want {
			t.Errorf("connAfter(%d) = %s", f, connAfter(f))
		}
	}
}

func TestPauseStopsRecording(t *testing.T) {
	ctx := context.Background()
	a := &Agent{Cfg: testCfg(), Buf: openBuf(t), Log: slog.New(slog.DiscardHandler),
		Sampler: SamplerFunc(func() (Sample, error) { return Sample{App: "Code.exe"}, nil })}
	a.SetPaused(true)
	if err := a.SampleOnce(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if bs, _ := a.Buf.Pending(ctx, 10); len(bs) != 0 {
		t.Fatalf("recorded while paused: %+v", bs)
	}
	a.SetPaused(false)
	_ = a.SampleOnce(ctx, 10*time.Second)
	if bs, _ := a.Buf.Pending(ctx, 10); len(bs) != 1 || bs[0].ActiveSeconds != 10 {
		t.Fatalf("after resume: %+v", bs)
	}
}
