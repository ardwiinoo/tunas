package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestDeviceTokenLifetime(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, _, err := db.CreateDeviceDays(ctx, "bad", 0); err == nil {
		t.Fatal("a lifetime of 0 days is refused")
	}
	d, tok, err := db.CreateDevice(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if d.ExpiresAt == nil || time.Until(*d.ExpiresAt) < 179*24*time.Hour || time.Until(*d.ExpiresAt) > 181*24*time.Hour {
		t.Fatalf("default lifetime is %d days, got expiry %v", store.DefaultTokenDays, d.ExpiresAt)
	}
	now := time.Now()
	if _, _, err := db.AuthDeviceIP(ctx, tok, now, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AuthDeviceIP(ctx, tok, now.Add(181*24*time.Hour), ""); !errors.Is(err, store.ErrExpired) {
		t.Fatalf("past the expiry: %v", err)
	}
	if _, _, err := db.AuthDeviceIP(ctx, "tnd_unknown", now, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := db.RevokeDevice(ctx, "laptop", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AuthDeviceIP(ctx, tok, now.Add(181*24*time.Hour), ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a revoked token is not reported as expired: %v", err)
	}
}

// The address is remembered, an empty one leaves it alone, and the previous
// address comes back with the next request.
func TestDeviceLastSeenIP(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	_, tok, _ := db.CreateDevice(ctx, "laptop")
	now := time.Now()
	_, prev, err := db.AuthDeviceIP(ctx, tok, now, "1.1.1.1")
	if err != nil || prev != "" {
		t.Fatalf("first: %q %v", prev, err)
	}
	_, prev, _ = db.AuthDeviceIP(ctx, tok, now, "2.2.2.2")
	if prev != "1.1.1.1" {
		t.Fatalf("prev = %q", prev)
	}
	d, prev, _ := db.AuthDeviceIP(ctx, tok, now, "")
	if prev != "2.2.2.2" || d.LastSeenIP != "2.2.2.2" {
		t.Fatalf("an empty address must keep the old one: prev %q, now %q", prev, d.LastSeenIP)
	}
}

func TestDeviceWarningsAreClaimedOnce(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	d, _, _ := db.CreateDeviceDays(ctx, "laptop", 20)
	now := time.Now()

	if ok, _ := db.ClaimExpiryWarning(ctx, d.ID, now); ok {
		t.Fatal("20 days left is not inside the 14-day window")
	}
	soon := now.Add(8 * 24 * time.Hour) // 12 days left
	if ok, err := db.ClaimExpiryWarning(ctx, d.ID, soon); !ok || err != nil {
		t.Fatalf("12 days left must be claimed: %v %v", ok, err)
	}
	if ok, _ := db.ClaimExpiryWarning(ctx, d.ID, soon.Add(time.Hour)); ok {
		t.Fatal("the expiry warning is sent once")
	}
	if ok, _ := db.ClaimExpiryWarning(ctx, d.ID, now.Add(30*24*time.Hour)); ok {
		t.Fatal("no warning after expiry")
	}

	if ok, _ := db.ClaimIPAlert(ctx, d.ID, now); !ok {
		t.Fatal("first address alert")
	}
	if ok, _ := db.ClaimIPAlert(ctx, d.ID, now.Add(23*time.Hour)); ok {
		t.Fatal("cooldown is 24 hours")
	}
	if ok, _ := db.ClaimIPAlert(ctx, d.ID, now.Add(25*time.Hour)); !ok {
		t.Fatal("after the cooldown")
	}
}
