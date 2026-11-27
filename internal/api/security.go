package api

import (
	"context"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/devices"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Failed-authentication warning (PRD 9b): this many failures inside the
// window warn the owner once per cooldown. Nothing is blocked.
const (
	failWindow   = 10 * time.Minute
	failLimit    = 10
	failCooldown = 24 * time.Hour
	notifyLimit  = 10 * time.Second
)

// authWatch counts failed authentications.
type authWatch struct {
	mu        sync.Mutex
	times     []time.Time
	lastAlert time.Time
}

// failed records one failure and reports whether the owner should be warned
// now (and with how many failures).
func (w *authWatch) failed(now time.Time) (warn bool, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	keep := w.times[:0]
	for _, t := range w.times {
		if now.Sub(t) < failWindow {
			keep = append(keep, t)
		}
	}
	w.times = append(keep, now)
	n = len(w.times)
	if n < failLimit || (!w.lastAlert.IsZero() && now.Sub(w.lastAlert) < failCooldown) {
		return false, n
	}
	w.lastAlert = now
	return true, n
}

// send delivers a warning to the owner without holding up the request.
func (s *Server) send(text string) {
	if s.notify == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyLimit)
		defer cancel()
		if err := s.notify(ctx, text); err != nil {
			s.log.Warn("security warning not sent", "err", err)
		}
	}()
}

// authFailed logs and counts a failed authentication. Only the address and
// the count are logged; the token never is.
func (s *Server) authFailed(ip, why string) {
	warn, n := s.watch.failed(s.now())
	s.log.Warn("device auth failed", "ip", ip, "reason", why, "failures_in_window", n)
	if warn {
		s.log.Warn("many failed authentications", "ip", ip, "count", n, "window_minutes", int(failWindow.Minutes()))
		s.send(devices.FailuresText(n, failWindow, ip))
	}
}

// authSucceeded sends the one-time warnings that depend on a good token:
// a new client address (once per 24 h) and the token expiring soon (once).
func (s *Server) authSucceeded(ctx context.Context, d store.Device, prevIP, ip string) {
	now := s.now()
	if prevIP != "" && ip != "" && prevIP != ip {
		if ok, err := s.db.ClaimIPAlert(ctx, d.ID, now); err != nil {
			s.log.Error("ip alert check failed", "err", err)
		} else if ok {
			s.log.Warn("device token used from a new address", "device", d.Name, "ip", ip, "previous", prevIP)
			s.send(devices.NewIPText(d.Name, prevIP, ip))
		}
	}
	if d.ExpiresAt != nil && d.ExpiresAt.Sub(now) <= store.ExpiryWarnDays*24*time.Hour {
		if ok, err := s.db.ClaimExpiryWarning(ctx, d.ID, now); err != nil {
			s.log.Error("expiry warning check failed", "err", err)
		} else if ok {
			s.send(devices.ExpiryText(d.Name, d.ExpiresAt.Sub(now)))
		}
	}
}
