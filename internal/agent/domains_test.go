package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type domAPI struct {
	API
	mu   sync.Mutex
	got  [][]DomainBucket
	fail bool
}

func (d *domAPI) PostDomains(_ context.Context, bs []DomainBucket) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail {
		return context.DeadlineExceeded
	}
	d.got = append(d.got, append([]DomainBucket(nil), bs...))
	return nil
}

func (d *domAPI) PostActivity(context.Context, []Bucket) error { return nil }

type domEnv struct {
	a      *Agent
	api    *domAPI
	logs   *bytes.Buffer
	now    time.Time
	app    string
	idle   time.Duration
	nudges []string
}

func newDomEnv(t *testing.T) *domEnv {
	e := &domEnv{api: &domAPI{}, logs: &bytes.Buffer{}, now: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC), app: "chrome.exe"}
	e.a = &Agent{
		Cfg: testCfg(), Buf: openBuf(t), API: e.api,
		Log:     slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sampler: SamplerFunc(func() (Sample, error) { return Sample{App: e.app, Idle: e.idle}, nil }),
		Now:     func() time.Time { return e.now },
	}
	e.a.init()
	e.a.SetBrowsers([]string{"chrome.exe", "msedge.exe"})
	e.a.OnNudge = func(text string) { e.nudges = append(e.nudges, text) }
	return e
}

func (e *domEnv) tick(t *testing.T, d time.Duration) {
	t.Helper()
	for end := e.now.Add(d); e.now.Before(end); {
		e.now = e.now.Add(10 * time.Second)
		if err := e.a.SampleOnce(context.Background(), 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *domEnv) pending(t *testing.T) []DomainBucket {
	t.Helper()
	bs, err := e.a.Buf.PendingDomains(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

func (e *domEnv) seconds(t *testing.T, domain string) int {
	n := 0
	for _, b := range e.pending(t) {
		if b.Domain == domain {
			n += b.ActiveSeconds
		}
	}
	return n
}

func TestDomainRecordingRules(t *testing.T) {
	e := newDomEnv(t)
	e.a.OnTab("youtube.com", e.now)
	e.tick(t, time.Minute)
	if len(e.pending(t)) != 0 {
		t.Fatal("recorded while switched off")
	}

	e.a.SetDomains(true)
	e.a.OnTab("www.YouTube.com", e.now)
	e.tick(t, time.Minute)
	if got := e.seconds(t, "youtube.com"); got != 60 {
		t.Fatalf("a minute on youtube = %d s", got)
	}

	// a hostname the agent does not accept is ignored, whatever the bridge did
	before := e.seconds(t, "youtube.com")
	for _, bad := range []string{"https://evil.com/x", "evil.com/path", "user:pw@evil.com", "10.0.0.1", "localhost"} {
		e.a.OnTab(bad, e.now)
	}
	e.tick(t, 10*time.Second)
	if e.seconds(t, "youtube.com") != before+10 || e.seconds(t, "evil.com") != 0 {
		t.Fatalf("a bad host replaced the current one: %v", e.pending(t))
	}

	// each of these records nothing
	for name, setup := range map[string]func(*domEnv){
		"idle owner":       func(e *domEnv) { e.idle = 5 * time.Minute },
		"not a browser":    func(e *domEnv) { e.app = "Code.exe" },
		"paused":           func(e *domEnv) { e.a.SetPaused(true) },
		"blocked":          func(e *domEnv) { e.a.setDomainRules([]string{"youtube.com"}, nil) },
		"no host from tab": func(e *domEnv) { e.a.OnTab("", e.now) },
	} {
		x := newDomEnv(t)
		x.a.SetDomains(true)
		x.a.OnTab("youtube.com", x.now)
		x.tick(t, 10*time.Second) // the browser has the focus, the signal is current
		for _, b := range x.pending(t) {
			_ = b
		}
		x.a.Buf.PurgeProjects(context.Background())   //nolint:errcheck
		x.a.Buf.db.Exec(`delete from domain_buckets`) //nolint:errcheck
		setup(x)
		x.tick(t, 30*time.Second)
		if n := len(x.pending(t)); n != 0 {
			t.Errorf("%s: recorded %d buckets (%v)", name, n, x.pending(t))
		}
	}

	// a blocked parent covers its subdomains
	b := newDomEnv(t)
	b.a.SetDomains(true)
	b.a.setDomainRules([]string{"example.com"}, nil)
	b.a.OnTab("bank.example.com", b.now)
	b.tick(t, 20*time.Second)
	if len(b.pending(t)) != 0 {
		t.Fatal("a subdomain of a blocked domain was recorded")
	}
	b.a.OnTab("notexample.com", b.now)
	b.tick(t, 20*time.Second)
	if b.seconds(t, "notexample.com") != 20 {
		t.Fatalf("a different domain must not be blocked: %v", b.pending(t))
	}
}

// A signal from before the browser got the focus is trusted for 15 seconds;
// one from after the focus stays valid for as long as nothing replaces it.
func TestStaleExtensionSignal(t *testing.T) {
	e := newDomEnv(t)
	e.a.SetDomains(true)
	e.a.OnTab("github.com", e.now.Add(-time.Minute)) // reported a minute ago, while another app had the focus
	e.app = "Code.exe"
	e.tick(t, 30*time.Second)
	e.app = "chrome.exe" // the browser comes to the front: the old signal is good for StaleTab, no longer
	e.tick(t, time.Minute)
	if got := e.seconds(t, "github.com"); got < 10 || got > 20 {
		t.Fatalf("an old signal counted %d s, want the grace of about %v only", got, StaleTab)
	}
	// the extension reports right after the focus change: valid for a long time
	e.a.OnTab("github.com", e.now)
	e.tick(t, 10*time.Minute)
	if got := e.seconds(t, "github.com"); got < 590 {
		t.Fatalf("a signal from after the focus must stay valid: %d s", got)
	}
}

func TestDomainTimeSyncsAndLeaksNothing(t *testing.T) {
	e := newDomEnv(t)
	e.a.SetDomains(true)
	e.a.OnTab("m.youtube.com", e.now)
	e.tick(t, 2*time.Minute)
	if _, err := e.a.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.api.mu.Lock()
	payload, _ := json.Marshal(e.api.got)
	e.api.mu.Unlock()
	if !strings.Contains(string(payload), "m.youtube.com") || strings.Contains(string(payload), "http") {
		t.Fatalf("payload %s", payload)
	}
	if strings.Contains(e.logs.String(), "youtube") {
		t.Fatalf("a site name reached the log: %s", e.logs.String())
	}
	if len(e.pending(t)) != 0 {
		t.Fatal("still pending after a successful sync")
	}
	// a failed upload keeps them for the retry
	e.tick(t, 20*time.Second)
	e.api.fail = true
	if _, err := e.a.flush(context.Background()); err == nil {
		t.Fatal("the failure must be reported")
	}
	if len(e.pending(t)) == 0 {
		t.Fatal("buckets were dropped by a failed upload")
	}
}

func TestDistractionNudgeFromASite(t *testing.T) {
	e := newDomEnv(t)
	e.a.SetDomains(true)
	e.a.setDomainRules(nil, map[string]string{"youtube.com": "distract", "github.com": "work"})
	e.a.OnTab("m.youtube.com", e.now) // matched by suffix
	e.tick(t, 29*time.Minute)
	if len(e.nudges) != 0 {
		t.Fatalf("nudged early: %v", e.nudges)
	}
	e.tick(t, 2*time.Minute)
	if len(e.nudges) != 1 || e.nudges[0] != "Udah 30 menit di m.youtube.com nih. Lagi belajar atau nonton?" {
		t.Fatalf("nudges %q", e.nudges)
	}
	// "Lagi belajar/kerja": quiet about that site for 90 minutes, even past the cooldown
	e.a.TegurAnswer(true)
	e.tick(t, 80*time.Minute)
	if len(e.nudges) != 1 {
		t.Fatalf("nudged during the snooze: %q", e.nudges)
	}
	e.tick(t, 15*time.Minute)
	if len(e.nudges) != 2 {
		t.Fatalf("the snooze ended but no nudge: %q", e.nudges)
	}
	// a site in the work category resets the distraction streak
	e.a.OnTab("github.com", e.now)
	e.tick(t, 5*time.Minute)
	if e.nudges = e.nudges[:2]; e.a.nudge.n.Streak() != 0 {
		t.Fatalf("working on github did not end the streak: %v", e.a.nudge.n.Streak())
	}
}

func TestDistractionNudgeFromAnApp(t *testing.T) {
	e := newDomEnv(t)
	e.app = "Discord.exe" // distract in the default config
	e.tick(t, 31*time.Minute)
	if len(e.nudges) != 1 || e.nudges[0] != "Udah 30 menit di Discord nih. Lagi belajar atau nonton?" {
		t.Fatalf("nudges %q", e.nudges)
	}
	// /tegur off from Telegram arrives with the state
	e2 := newDomEnv(t)
	e2.app = "Discord.exe"
	e2.a.setTegurEnabled(false)
	e2.tick(t, 2*time.Hour)
	if len(e2.nudges) != 0 {
		t.Fatalf("nudged while switched off: %q", e2.nudges)
	}
	e2.a.setTegurEnabled(true)
	e2.tick(t, 10*time.Second)
	if len(e2.nudges) != 1 {
		t.Fatal("switching on again nudges a streak that is still going")
	}
	// paused: nothing is counted
	e3 := newDomEnv(t)
	e3.app = "Discord.exe"
	e3.a.SetPaused(true)
	e3.tick(t, 2*time.Hour)
	if len(e3.nudges) != 0 {
		t.Fatalf("nudged while tracking is paused: %q", e3.nudges)
	}
}

func TestBridgeStaysOffWithoutTheSwitch(t *testing.T) {
	e := newDomEnv(t)
	if e.a.DomainsOn() {
		t.Fatal("website recording must start off")
	}
	e.a.SetDomains(true)
	e.a.OnTab("github.com", e.now)
	e.a.SetDomains(false) // turning it off forgets the current site
	e.a.SetDomains(true)
	e.tick(t, 20*time.Second)
	if len(e.pending(t)) != 0 {
		t.Fatalf("a site from before the switch-off was recorded: %v", e.pending(t))
	}
}
