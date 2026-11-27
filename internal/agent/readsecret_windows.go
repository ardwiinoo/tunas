//go:build windows

package agent

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// ReadSecretLine reads one line from stdin with console echo switched off, so
// a pasted token does not stay on screen. When stdin is a pipe (not a
// console) it is just read.
func ReadSecretLine() (string, error) {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		if windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT) == nil {
			defer windows.SetConsoleMode(h, mode) //nolint:errcheck
		}
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
