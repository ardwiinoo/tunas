//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestSingleInstance(t *testing.T) {
	name := fmt.Sprintf("tunas-test-%d", os.Getpid())
	release, err := SingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SingleInstance(name); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second instance: %v", err)
	}
	release()
	again, err := SingleInstance(name)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}
