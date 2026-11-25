package overlay

import (
	"fmt"
	"image/color"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// Tray texts and colours (platform-independent so they can be tested).

// TrayTip is the tray tooltip for a connection state.
func TrayTip(s agent.ConnState, paused bool) string {
	t := "Tunas: "
	switch s {
	case agent.ConnOnline:
		t += "tersambung"
	case agent.ConnConnecting:
		t += "menyambung ulang…"
	default:
		t += "offline"
	}
	if paused {
		t += " (pelacakan dijeda)"
	}
	return t
}

// TrayColor is the status dot colour: green, yellow, red.
func TrayColor(s agent.ConnState) color.RGBA {
	switch s {
	case agent.ConnOnline:
		return color.RGBA{0x3F, 0xB9, 0x50, 0xFF}
	case agent.ConnConnecting:
		return color.RGBA{0xE3, 0xB3, 0x41, 0xFF}
	}
	return color.RGBA{0xE5, 0x53, 0x4B, 0xFF}
}

// Tray menu items.
const (
	MenuPause = "Jeda pelacakan"
	MenuLog   = "Buka log"
	MenuQuit  = "Keluar"
)

// ColPaused is the icon while tracking is paused.
var ColPaused = color.RGBA{0x9A, 0xA0, 0xA6, 0xFF}

// TrayIcon draws the status dot as straight-alpha BGRA pixels (the layout
// CreateIconIndirect expects for a 32-bit icon), size×size.
func TrayIcon(s agent.ConnState, size int) []byte { return TrayIconColor(TrayColor(s), size) }

// TrayIconColor draws the dot in any colour (grey when paused).
func TrayIconColor(col color.RGBA, size int) []byte {
	c := NewCanvas(size, size)
	r := float32(size) * 0.36
	cx := float32(size) / 2
	var p Path
	p.Ellipse(cx, cx, r, r)
	c.Fill(&p, col)
	c.Stroke(&p, max(1, float32(size)/16), color.RGBA{0x20, 0x20, 0x20, 0xFF})
	src := c.Img.Pix
	out := make([]byte, len(src))
	for i := 0; i+3 < len(src); i += 4 {
		a := src[i+3]
		if a == 0 {
			continue
		}
		un := func(v byte) byte { return byte(min(255, int(v)*255/int(a))) } // un-premultiply
		out[i], out[i+1], out[i+2], out[i+3] = un(src[i+2]), un(src[i+1]), un(src[i]), a
	}
	return out
}

// Tray menu commands.
const (
	CmdNone = iota
	CmdAutostart
	CmdAutostartFix
	CmdPause
	CmdWorklogBubbles
	CmdProjects
	CmdWriteWorklog
	CmdClipboard
	CmdMuteWorklog
	CmdStatus
	CmdCharacter
	CmdLog
	CmdConfigDir
	CmdQuit
)

// MenuItem is one line of the tray menu.
type MenuItem struct {
	ID      int
	Text    string
	Checked bool
	Grayed  bool
	Sep     bool
}

// MenuInfo is what the menu shows, read when it opens.
type MenuInfo struct {
	Status         string // "Tunas · online · sinkron 12 d lalu"
	Queued         int    // worklog entries waiting to sync
	AutostartAvail bool
	AutostartOn    bool
	AutostartStale bool // the Run value points at another exe
	Paused         bool
	WorklogBubbles bool
	Projects       bool // "Catat proyek editor"
	CharacterShown bool
	HasLog         bool
	HasConfigDir   bool
}

// TrayMenu builds the menu (PRD 8.4).
func TrayMenu(i MenuInfo) []MenuItem {
	items := []MenuItem{{Text: i.Status, Grayed: true}}
	if i.Queued > 0 {
		items = append(items, MenuItem{Text: fmt.Sprintf("%d catatan worklog menunggu sinkron", i.Queued), Grayed: true})
	}
	items = append(items, MenuItem{Sep: true})
	if i.AutostartAvail {
		items = append(items, MenuItem{ID: CmdAutostart, Text: "Jalankan saat Windows nyala", Checked: i.AutostartOn})
		if i.AutostartStale {
			items = append(items, MenuItem{ID: CmdAutostartFix, Text: "Perbarui path autostart (exe sudah pindah)"})
		}
	}
	char := "Sembunyikan karakter"
	if !i.CharacterShown {
		char = "Tampilkan karakter"
	}
	items = append(items,
		MenuItem{ID: CmdPause, Text: MenuPause, Checked: i.Paused},
		MenuItem{ID: CmdWorklogBubbles, Text: "Pengingat worklog di layar", Checked: i.WorklogBubbles},
		MenuItem{ID: CmdProjects, Text: "Catat proyek editor", Checked: i.Projects},
		MenuItem{ID: CmdWriteWorklog, Text: "Tulis worklog…"},
		MenuItem{ID: CmdClipboard, Text: "Tulis worklog dari clipboard"},
		MenuItem{ID: CmdMuteWorklog, Text: "Senyapkan worklog hari ini"},
		MenuItem{ID: CmdStatus, Text: "Status…"},
		MenuItem{ID: CmdCharacter, Text: char},
		MenuItem{Sep: true},
		MenuItem{ID: CmdLog, Text: MenuLog, Grayed: !i.HasLog},
		MenuItem{ID: CmdConfigDir, Text: "Buka folder konfigurasi", Grayed: !i.HasConfigDir},
		MenuItem{ID: CmdQuit, Text: MenuQuit},
	)
	return items
}
