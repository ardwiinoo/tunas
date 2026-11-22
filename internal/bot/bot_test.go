package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestSplitCommand(t *testing.T) {
	tests := []struct{ in, cmd, args string }{
		{"/task beli kopi", "task", "beli kopi"},
		{"/Task@TunasBot  beli kopi ", "task", "beli kopi"},
		{"/tasks", "tasks", ""},
	}
	for _, tt := range tests {
		cmd, args, ok := SplitCommand(tt.in)
		if !ok || cmd != tt.cmd || args != tt.args {
			t.Errorf("SplitCommand(%q) = %q %q %v", tt.in, cmd, args, ok)
		}
	}
	for _, in := range []string{"halo", "", "/"} {
		if _, _, ok := SplitCommand(in); ok {
			t.Errorf("%q should not be a command", in)
		}
	}
}

func TestIsOwner(t *testing.T) {
	msg := func(from, chat int64) *models.Update {
		return &models.Update{Message: &models.Message{From: &models.User{ID: from}, Chat: models.Chat{ID: chat}}}
	}
	if !IsOwner(msg(42, 42), 42) {
		t.Error("owner private chat rejected")
	}
	if IsOwner(msg(7, 7), 42) {
		t.Error("stranger accepted")
	}
	if IsOwner(msg(42, -100123), 42) {
		t.Error("owner in group chat accepted")
	}
	if IsOwner(&models.Update{Message: &models.Message{Chat: models.Chat{ID: 42}}}, 42) {
		t.Error("message without sender accepted")
	}
	if IsOwner(&models.Update{CallbackQuery: &models.CallbackQuery{From: models.User{ID: 7}}}, 42) {
		t.Error("stranger callback accepted")
	}
	if IsOwner(&models.Update{}, 42) {
		t.Error("empty update accepted")
	}
}

func TestRedactToken(t *testing.T) {
	tok := "123456:ABC-secret"
	err := fmt.Errorf(`Post "https://api.telegram.org/bot%s/getUpdates": timeout`, tok)
	got := RedactToken(err.Error(), tok)
	if strings.Contains(got, "ABC-secret") || !strings.Contains(got, "<redacted>") {
		t.Fatalf("got %q", got)
	}
	tg := &Telegram{token: tok}
	if strings.Contains(tg.Redact(errors.New("x "+tok)), tok) {
		t.Fatal("Redact leaked token")
	}
}

func TestCommandsFlow(t *testing.T) {
	db := storetest.New(t)
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, loc)
	c := &Commands{DB: db, Loc: loc, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()

	expect := func(in, want string) string {
		t.Helper()
		got := c.Handle(ctx, in)
		if !strings.Contains(got, want) {
			t.Fatalf("%s → %q, want contains %q", in, got, want)
		}
		return got
	}

	expect("/help", "/remind")
	expect("halo", "/help")
	expect("/task", "Pakai")
	reply := expect("/task beli kopi", "dicatat: beli kopi")
	id := regexp.MustCompile(`#(\d+)`).FindStringSubmatch(reply)[1]
	expect("/tasks", id+". beli kopi")
	expect("/done "+id, "Selesai: beli kopi")
	expect("/done "+id, "sudah selesai")
	expect("/done abc", "Pakai")
	expect("/tasks", "Tidak ada tugas")
	expect("/task "+strings.Repeat("a", MaxTitleLen+1), "terlalu panjang")

	reply = expect("/remind 17:00 kirim laporan", "Oke, diingatkan Sen 23 Nov 17:00 WIB: kirim laporan")
	rid := regexp.MustCompile(`\[#(\d+)\]`).FindStringSubmatch(reply)[1]
	expect("/remind tiap senin 08:00 review", "(tiap Senin)")
	expect("/remind kapan-kapan x", "Tidak bisa membaca waktu")
	expect("/reminders", "kirim laporan")
	expect("/cancel "+rid, "Dibatalkan: kirim laporan")
	expect("/cancel "+rid, "tidak ada")
	expect("/ping", "belum ada")
	expect("/unknown", "tidak dikenal")
}
