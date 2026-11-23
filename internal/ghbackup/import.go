package ghbackup

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/worklog"
)

var reMonthFile = regexp.MustCompile(`^(\d{4})(\d{2})_daily\.txt$`)

// FileReport is the import summary of one monthly file (counts only, never
// the entries themselves).
type FileReport struct {
	Name       string
	Days       int
	Entries    int
	New        int // inserted (or would be, in a dry run)
	Existing   int // same date + text already in Tunas
	Unreadable int
	OtherMonth int  // entries whose date is outside the file's month
	SameRender bool // Render(month) equals the original file (after import)
}

// Import reads every YYYYMM_daily.txt in the backup folder and inserts the
// entries (source "import"). It is idempotent: an entry with the same date
// and text is skipped. The day order of each file is kept (see
// store.WorklogMonth), so rendering a month afterwards gives the file back.
// A dry run reads and counts without writing.
func Import(ctx context.Context, gh Repo, db *store.DB, dryRun bool, now time.Time, out io.Writer) ([]FileReport, error) {
	names, err := gh.List(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var reports []FileReport
	for _, name := range names {
		m := reMonthFile.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		month, err := time.Parse("200601", m[1]+m[2])
		if err != nil {
			continue
		}
		f, err := gh.Get(ctx, name)
		if err != nil {
			return reports, fmt.Errorf("%s: %w", name, err)
		}
		rep, err := importFile(ctx, db, month, f, dryRun, now)
		if err != nil {
			return reports, fmt.Errorf("%s: %w", name, err)
		}
		reports = append(reports, rep)
		same := "-"
		if !dryRun {
			same = map[bool]string{true: "ya", false: "TIDAK"}[rep.SameRender]
		}
		fmt.Fprintf(out, "%s: %d hari, %d entri (%d baru, %d sudah ada), %d baris tak terbaca, %d di luar bulan, render sama: %s\n",
			name, rep.Days, rep.Entries, rep.New, rep.Existing, rep.Unreadable, rep.OtherMonth, same)
	}
	total := FileReport{}
	for _, r := range reports {
		total.Days += r.Days
		total.Entries += r.Entries
		total.New += r.New
		total.Existing += r.Existing
		total.Unreadable += r.Unreadable
	}
	mode := "diimpor"
	if dryRun {
		mode = "akan diimpor (dry run, tidak ada yang ditulis)"
	}
	fmt.Fprintf(out, "Total: %d berkas, %d hari, %d entri; %d %s, %d sudah ada, %d baris tak terbaca\n",
		len(reports), total.Days, total.Entries, total.New, mode, total.Existing, total.Unreadable)
	return reports, nil
}

func importFile(ctx context.Context, db *store.DB, month time.Time, f File, dryRun bool, now time.Time) (FileReport, error) {
	res := worklog.Parse(f.Content)
	rep := FileReport{Name: f.Name, Days: len(res.Days), Unreadable: res.Unreadable}
	// created_at follows the file order (all before any new entry), which is
	// what WorklogMonth sorts days by
	base := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	seq := 0
	for _, d := range res.Days {
		if d.Date.Year() != month.Year() || d.Date.Month() != month.Month() {
			rep.OtherMonth += len(d.Bullets)
		}
		for _, text := range d.Bullets {
			seq++
			rep.Entries++
			if utf8.RuneCountInString(text) > worklog.MaxTextLen {
				rep.Unreadable++ // longer than the database allows: stays in the file only
				continue
			}
			exists, err := db.WorklogExists(ctx, d.Date, text)
			if err != nil {
				return rep, err
			}
			if exists {
				rep.Existing++
				continue
			}
			rep.New++
			if dryRun {
				continue
			}
			if _, err := db.AddWorklogAt(ctx, d.Date, text, "import", base.Add(time.Duration(seq)*time.Second)); err != nil {
				return rep, err
			}
		}
	}
	if dryRun {
		return rep, nil
	}
	content, _, err := MonthContent(ctx, db, month)
	if err != nil {
		return rep, err
	}
	rep.SameRender = content == f.Content
	// the repo already has this file: remember its sha so the next push
	// updates it; push only if Tunas has more than the file
	err = db.SyncImported(ctx, month.Format("2006-01"), f.SHA, hashOf(f.Content), !rep.SameRender, now)
	return rep, err
}
