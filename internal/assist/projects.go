package assist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/projreport"
)

// projectFacts renders the top_projects template: time per project.
func (a *Assistant) projectFacts(ctx context.Context, from, to time.Time) (string, error) {
	ts, err := a.DB.ProjectTotalsBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "Belum ada waktu proyek pada rentang ini (pencatatan proyek editor mati atau belum ada data).", nil
	}
	var b strings.Builder
	b.WriteString("Proyek teratas (waktu aktif di editor):")
	for i, t := range ts {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "\n%d. %s", i+1, projreport.Line(t))
	}
	return b.String(), nil
}

// includeProjects reports whether project names may be sent to the LLM
// (settings llm.include_projects, default off).
func (a *Assistant) includeProjects(ctx context.Context) bool {
	on := false
	if _, err := a.DB.GetSetting(ctx, projreport.KeyIncludeLLM, &on); err != nil {
		return false
	}
	return on
}
