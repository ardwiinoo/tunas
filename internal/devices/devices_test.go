package devices

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestMaskIP(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":          "203.0.*.*",
		"2001:db8:1:2::9":      "2001:db8:*",
		"::1":                  "0:0:*",
		"::ffff:198.51.100.23": "198.51.*.*",
		"":                     "?",
		"not an ip":            "?",
		"1.2.3":                "?",
	} {
		if got := MaskIP(in); got != want {
			t.Errorf("MaskIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func rig(t *testing.T) (*Service, *store.DB, string, time.Time) {
	db := storetest.New(t)
	ctx := context.Background()
	d, token, err := db.CreateDeviceDays(ctx, "laptop", 100)
	if err != nil {
		t.Fatal(err)
	}
	_ = d
	now := time.Now()
	if _, _, err := db.AuthDeviceIP(ctx, token, now.Add(-3*time.Minute), "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateDeviceDays(ctx, "tablet", 100); err != nil {
		t.Fatal(err)
	}
	return &Service{DB: db, Now: func() time.Time { return now }, Loc: time.UTC}, db, token, now
}

func TestDevicesListMasksTheAddress(t *testing.T) {
	s, _, token, _ := rig(t)
	r, err := s.Command(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"laptop", "tablet", "203.0.*.*", "3 mnt lalu", "belum pernah", "kedaluwarsa 20", "hari lagi)", "--set-token"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("the list lacks %q:\n%s", want, r.Text)
		}
	}
	if strings.Contains(r.Text, "203.0.113.7") || strings.Contains(r.Text, token) || strings.Contains(r.Text, store.HashToken(token)) {
		t.Fatalf("the list shows a full address or a token:\n%s", r.Text)
	}
	if len(r.Rows) != 2 || !strings.HasPrefix(r.Rows[0][0].Data, "dv:rv:") {
		t.Fatalf("buttons %+v", r.Rows)
	}
	for _, row := range r.Rows {
		if len(row[0].Data) > 64 {
			t.Fatal("callback data too long")
		}
	}
}

// Revoking takes two taps: the first only asks, the second revokes.
func TestRevokeNeedsConfirmation(t *testing.T) {
	s, db, token, _ := rig(t)
	ctx := context.Background()
	list, _ := s.Command(ctx, "")
	ask := list.Rows[0][0].Data // laptop

	r, ok, err := s.Callback(ctx, ask)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if !strings.Contains(r.Text, `"laptop"`) || len(r.Buttons) != 2 || r.Buttons[0].Data != "dv:ok:"+strings.TrimPrefix(ask, "dv:rv:") || r.Buttons[1].Data != "dv:no" {
		t.Fatalf("confirmation %+v", r)
	}
	if _, err := db.AuthDevice(ctx, token, time.Now()); err != nil {
		t.Fatalf("asking must not revoke: %v", err)
	}

	// cancel goes back to the list, nothing revoked
	back, ok, err := s.Callback(ctx, "dv:no")
	if !ok || err != nil || !strings.Contains(back.Text, "Perangkat aktif (2)") {
		t.Fatalf("%v %v %q", ok, err, back.Text)
	}
	if _, err := db.AuthDevice(ctx, token, time.Now()); err != nil {
		t.Fatal("cancel must not revoke")
	}

	done, ok, err := s.Callback(ctx, r.Buttons[0].Data)
	if !ok || err != nil || !strings.Contains(done.Text, "dicabut") {
		t.Fatalf("%v %v %q", ok, err, done.Text)
	}
	if _, err := db.AuthDevice(ctx, token, time.Now()); err == nil {
		t.Fatal("the token still works after revoke")
	}
	// tapping again, or an old confirmation, is harmless
	again, ok, _ := s.Callback(ctx, r.Buttons[0].Data)
	if !ok || !again.Keep || again.Text != "" {
		t.Fatalf("a second tap: %+v", again)
	}
	if left, _ := s.Command(ctx, ""); !strings.Contains(left.Text, "1. tablet") || len(left.Rows) != 1 || left.Rows[0][0].Text != "Cabut tablet" {
		t.Fatalf("after revoke:\n%s", left.Text)
	}
}

func TestCallbackIgnoresForeignAndBadData(t *testing.T) {
	s, _, _, _ := rig(t)
	ctx := context.Background()
	if _, ok, _ := s.Callback(ctx, "st:x"); ok {
		t.Fatal("not ours must pass through")
	}
	for _, bad := range []string{"dv:rv:abc", "dv:ok:", "dv:ok:99999999", fmt.Sprintf("dv:rv:%d", 1<<62)} {
		r, ok, err := s.Callback(ctx, bad)
		if !ok || err != nil || !r.Keep {
			t.Errorf("%q → %+v ok=%v err=%v", bad, r, ok, err)
		}
	}
}

func TestEmptyListAndArgs(t *testing.T) {
	db := storetest.New(t)
	s := &Service{DB: db, Now: time.Now, Loc: time.UTC}
	if r, err := s.Command(context.Background(), ""); err != nil || !strings.Contains(r.Text, "Belum ada perangkat") {
		t.Fatalf("%v %q", err, r.Text)
	}
	if r, _ := s.Command(context.Background(), "hapus semua"); !strings.Contains(r.Text, "Pakai: /devices") {
		t.Fatalf("%q", r.Text)
	}
}
