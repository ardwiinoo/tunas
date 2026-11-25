//go:build !windows

package overlay

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// ChatAPI is the server side of the bubble (agent.Client implements it).
type ChatAPI interface {
	Chat(ctx context.Context, text, form string, history []agent.ChatTurn) (agent.ChatReply, error)
	Confirm(ctx context.Context, id string, yes bool) (string, error)
}

// Overlay is unavailable outside Windows.
type Overlay struct {
	CharacterOff bool
	OnPause      func(bool)
	OnQuit       func()
	LogPath      string
}

// Configure does nothing.
func (*Overlay) Configure(time.Duration, time.Duration, bool, LoadingLines) {}

// ConfigureSpeech does nothing.
func (*Overlay) ConfigureSpeech(time.Duration, time.Duration, time.Duration) {}

// SetIdleClose does nothing.
func (*Overlay) SetIdleClose(time.Duration) {}

// SetConn does nothing.
func (*Overlay) SetConn(agent.ConnState) {}

// New returns an error: the overlay is Windows-only.
func New(config.AgentOverlay, ChatAPI, *slog.Logger) (*Overlay, error) {
	return nil, errors.New("overlay is only supported on Windows")
}

// Notify does nothing.
func (*Overlay) Notify(forms.Output) {}

// Run does nothing.
func (*Overlay) Run(context.Context) error { return nil }

// SetTray does nothing.
func (*Overlay) SetTray(TrayOptions) {}

// WorklogEvent does nothing.
func (*Overlay) WorklogEvent(agent.WorklogEvent) {}

// SetWorklogQueue does nothing.
func (*Overlay) SetWorklogQueue(int) {}
