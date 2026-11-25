//go:build !windows

package projects

import (
	"context"
	"errors"
	"io"
	"time"
)

// NewReader returns a reader that always fails: titles are Windows-only.
func NewReader() TitleReader {
	return ReaderFunc(func() (string, string, error) { return "", "", errors.New("window titles are only read on Windows") })
}

// Probe does nothing outside Windows.
func Probe(context.Context, io.Writer, TitleReader, time.Duration) {}
