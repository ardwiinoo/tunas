package overlay

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/ardwiinoo/tunas/internal/agent"
)

func TestTrayIconAndTip(t *testing.T) {
	seen := map[[3]byte]bool{}
	for _, st := range []agent.ConnState{agent.ConnOnline, agent.ConnConnecting, agent.ConnOffline} {
		const size = 32
		px := TrayIcon(st, size)
		if len(px) != size*size*4 {
			t.Fatalf("%s: %d bytes", st, len(px))
		}
		at := func(x, y int) []byte { o := (y*size + x) * 4; return px[o : o+4] }
		if at(0, 0)[3] != 0 {
			t.Errorf("%s: corner not transparent", st)
		}
		c, want := at(size/2, size/2), TrayColor(st)
		if c[0] != want.B || c[1] != want.G || c[2] != want.R || c[3] != 255 {
			t.Errorf("%s: centre %v, want BGRA of %v", st, c, want)
		}
		seen[[3]byte{want.R, want.G, want.B}] = true
		if dir := os.Getenv("TUNAS_RENDER_DIR"); dir != "" { // visual review
			img := image.NewNRGBA(image.Rect(0, 0, size, size))
			for i := 0; i < len(px); i += 4 {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = px[i+2], px[i+1], px[i], px[i+3]
			}
			fh, _ := os.Create(filepath.Join(dir, "tray-"+st.String()+".png"))
			_ = png.Encode(fh, img)
			fh.Close()
		}
	}
	if len(seen) != 3 {
		t.Fatal("states must have distinct colours")
	}
	if TrayTip(agent.ConnOnline, false) != "Tunas: tersambung" ||
		TrayTip(agent.ConnConnecting, false) != "Tunas: menyambung ulang…" ||
		TrayTip(agent.ConnOffline, true) != "Tunas: offline (pelacakan dijeda)" {
		t.Fatal("tooltips")
	}
}
