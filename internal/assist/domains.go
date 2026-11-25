package assist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/domaincmd"
)

// domainFacts renders the top_domains template: time per website.
func (a *Assistant) domainFacts(ctx context.Context, from, to time.Time) (string, error) {
	ts, err := a.DB.DomainTotalsBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "Belum ada data situs pada rentang ini (pencatatan domain mati atau belum ada data).", nil
	}
	var b strings.Builder
	b.WriteString("Situs teratas (waktu aktif):")
	for i, t := range ts {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "\n%d. %s — %s", i+1, t.Domain, activity.FormatDuration(time.Duration(t.Seconds)*time.Second))
	}
	return b.String(), nil
}

// includeDomains reports whether website names may be sent to the LLM
// (settings llm.include_domains, default off).
func (a *Assistant) includeDomains(ctx context.Context) bool {
	on := false
	if _, err := a.DB.GetSetting(ctx, domaincmd.KeyIncludeLLM, &on); err != nil {
		return false
	}
	return on
}

// domainSection is the "Situs teratas" block of a day ("" when there is no data).
func (a *Assistant) domainSection(ctx context.Context, from, to time.Time) string {
	ts, err := a.DB.DomainTotalsBetween(ctx, from, to)
	if err != nil {
		return ""
	}
	return domaincmd.Section(ts, 5)
}
