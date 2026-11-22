//go:build !windows

package overlay

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// ChatFunc sends typed text to the server and returns Tunas's reply.
type ChatFunc func(ctx context.Context, text string, f forms.Form) (string, error)

// Overlay is unavailable outside Windows.
type Overlay struct{}

// New returns an error: the overlay is Windows-only.
func New(config.AgentOverlay, ChatFunc, *slog.Logger) (*Overlay, error) {
	return nil, errors.New("overlay is only supported on Windows")
}

// Notify does nothing.
func (*Overlay) Notify(forms.Output) {}

// Run does nothing.
func (*Overlay) Run(context.Context) error { return nil }
