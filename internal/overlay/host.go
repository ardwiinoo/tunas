package overlay

import (
	"context"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// Host is the agent side the tray and the worklog bubble need.
type Host interface {
	// WriteWorklog saves an entry (local queue first, then the server).
	WriteWorklog(ctx context.Context, text string, promptID int64) (agent.WriteResult, error)
	// AnswerPrompt answers a worklog reminder: "later" | "skip".
	AnswerPrompt(ctx context.Context, id int64, action string) error
	// Status is the tray's first line, e.g. "Tunas · online · sinkron 12 d lalu".
	Status() string
	// SaveState persists the tray switches (state.json).
	SaveState(st agent.UIState)
}

// TrayOptions are set before Run.
type TrayOptions struct {
	Host      Host
	State     agent.UIState  // initial switches from state.json
	Registry  agent.Registry // autostart (nil = item hidden)
	ExePath   string         // for the autostart value
	ConfigDir string         // "Buka folder konfigurasi"
	// OfferAutostart shows the first-run question once (not with --background).
	OfferAutostart bool
}
