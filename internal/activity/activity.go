// Package activity aggregates activity buckets into summaries.
package activity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// SessionGap is the max gap between buckets of the same app within one session.
const SessionGap = 2 * time.Minute

// AppTotal is active time for one app.
type AppTotal struct {
	App      string
	Category string
	Active   time.Duration
}

// Session is a run of consecutive minutes on one app.
type Session struct {
	App        string
	Start, End time.Time
}

// Duration is the session's wall-clock span.
func (s Session) Duration() time.Duration { return s.End.Sub(s.Start) }

// Summary is an aggregate over a time range.
type Summary struct {
	TotalActive time.Duration
	Apps        []AppTotal // sorted by Active desc
	ByCategory  map[string]time.Duration
	Longest     *Session
}

// Summarize aggregates buckets. Buckets from multiple devices are merged.
func Summarize(bs []store.Bucket) Summary {
	s := Summary{ByCategory: map[string]time.Duration{}}
	apps := map[string]*AppTotal{}
	perApp := map[string][]time.Time{}
	for _, b := range bs {
		if b.ActiveSeconds <= 0 {
			continue
		}
		d := time.Duration(b.ActiveSeconds) * time.Second
		s.TotalActive += d
		s.ByCategory[b.Category] += d
		a := apps[b.App]
		if a == nil {
			a = &AppTotal{App: b.App, Category: b.Category}
			apps[b.App] = a
		}
		a.Active += d
		perApp[b.App] = append(perApp[b.App], b.Start)
	}
	for _, a := range apps {
		s.Apps = append(s.Apps, *a)
	}
	sort.Slice(s.Apps, func(i, j int) bool {
		if s.Apps[i].Active != s.Apps[j].Active {
			return s.Apps[i].Active > s.Apps[j].Active
		}
		return s.Apps[i].App < s.Apps[j].App
	})
	s.Longest = longestSession(perApp)
	return s
}

// longestSession finds the longest run of minutes on one app where the gap
// between one bucket's end and the next bucket's start is ≤ SessionGap.
func longestSession(perApp map[string][]time.Time) *Session {
	var best *Session
	names := make([]string, 0, len(perApp))
	for app := range perApp {
		names = append(names, app)
	}
	sort.Strings(names) // deterministic tie-breaking
	for _, app := range names {
		starts := perApp[app]
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
		cur := Session{App: app, Start: starts[0], End: starts[0].Add(time.Minute)}
		consider := func(c Session) {
			if best == nil || c.Duration() > best.Duration() {
				cp := c
				best = &cp
			}
		}
		for _, st := range starts[1:] {
			if st.Sub(cur.End) <= SessionGap {
				if e := st.Add(time.Minute); e.After(cur.End) {
					cur.End = e
				}
				continue
			}
			consider(cur)
			cur = Session{App: app, Start: st, End: st.Add(time.Minute)}
		}
		consider(cur)
	}
	return best
}

// DayRange returns [start of day, start of next day) in loc for t.
func DayRange(t time.Time, loc *time.Location) (time.Time, time.Time) {
	t = t.In(loc)
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}

// CategoryLabel renders a category in Indonesian.
func CategoryLabel(c string) string {
	switch c {
	case "work":
		return "kerja"
	case "browse":
		return "jelajah"
	case "distract":
		return "hiburan"
	}
	return "lainnya"
}

// FormatDuration renders e.g. "3 j 20 m", "45 m", "< 1 m".
func FormatDuration(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m == 0:
		return "< 1 m"
	case m < 60:
		return fmt.Sprintf("%d m", m)
	case m%60 == 0:
		return fmt.Sprintf("%d j", m/60)
	}
	return fmt.Sprintf("%d j %d m", m/60, m%60)
}

// FormatToday renders the /today reply.
func FormatToday(s Summary, title string, loc *time.Location) string {
	if s.TotalActive == 0 {
		return title + "\nBelum ada data aktivitas hari ini."
	}
	var b strings.Builder
	b.WriteString(title)
	fmt.Fprintf(&b, "\nTotal aktif: %s", FormatDuration(s.TotalActive))

	b.WriteString("\n\nAplikasi teratas:")
	for i, a := range s.Apps {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "\n%d. %s — %s", i+1, a.App, FormatDuration(a.Active))
	}

	if l := s.Longest; l != nil {
		fmt.Fprintf(&b, "\n\nSesi terpanjang: %s %s (%s–%s)", l.App, FormatDuration(l.Duration()),
			l.Start.In(loc).Format("15:04"), l.End.In(loc).Format("15:04"))
	}

	b.WriteString("\n\nPer kategori:")
	for _, c := range []string{"work", "browse", "distract", "other"} {
		if d := s.ByCategory[c]; d > 0 {
			fmt.Fprintf(&b, "\n• %s: %s", CategoryLabel(c), FormatDuration(d))
		}
	}
	return b.String()
}
