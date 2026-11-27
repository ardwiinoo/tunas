//go:build !windows

package agent

import (
	"bufio"
	"os"
	"strings"
)

// ReadSecretLine reads one line from stdin.
func ReadSecretLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
