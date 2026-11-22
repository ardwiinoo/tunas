package modes

import (
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/forms"
)

func TestLoadAllForms(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range forms.All {
		m, ok := ms[string(f)]
		if !ok || !strings.Contains(m.Voice, "Tunas") {
			t.Errorf("mode %s missing or invalid: %+v", f, m)
		}
	}
}
