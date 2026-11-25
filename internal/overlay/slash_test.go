package overlay

import "testing"

func TestSlashCommandParsing(t *testing.T) {
	for in, want := range map[string][3]string{
		"/today":                        {"today", "", "ok"},
		"  /Rekap 2026-10 ":             {"rekap", "2026-10", "ok"},
		"/done 12":                      {"done", "12", "ok"},
		"/form  fokus":                  {"form", "fokus", "ok"},
		"/cari harga emas":              {"cari", "harga emas", "ok"},
		"/status":                       {"status", "", "ok"},
		"/":                             {"", "", ""},
		"/ today":                       {"", "", ""},
		"/123":                          {"", "", ""},
		"/../etc":                       {"", "", ""},
		"today":                         {"", "", ""},
		"jam berapa /today":             {"", "", ""},
		"/aaaaaaaaaaaaaaaaaaaaaaaaaaaa": {"", "", ""},
	} {
		name, args, ok := SlashCommand(in)
		if (want[2] == "ok") != ok || name != want[0] || args != want[1] {
			t.Errorf("SlashCommand(%q) = %q, %q, %v", in, name, args, ok)
		}
	}
	for in, want := range map[string]bool{"/cal": true, " /CAL ": true, "/cal 2026": false, "/calendar": false, "cal": false} {
		if IsCalCommand(in) != want {
			t.Errorf("IsCalCommand(%q) = %v", in, !want)
		}
	}
}

func TestTrayMenuHasTheCalendarAndStatus(t *testing.T) {
	var cal, st bool
	for _, it := range TrayMenu(MenuInfo{Status: "Tunas"}) {
		switch it.ID {
		case CmdCalendar:
			cal = it.Text == "Kalender worklog…"
		case CmdStatus:
			st = true
		}
	}
	if !cal || !st {
		t.Fatalf("calendar item %v, status item %v", cal, st)
	}
}
