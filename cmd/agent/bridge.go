package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/bridge"
	"github.com/ardwiinoo/tunas/internal/config"
)

// bridgeCtl runs the local bridge for the browser extension only while
// "Catat domain situs" is on, so no port is open when it is off.
type bridgeCtl struct {
	parent context.Context
	cfg    config.AgentExtension
	token  string
	a      *agent.Agent
	log    *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (b *bridgeCtl) Set(on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !on {
		if b.cancel != nil {
			b.cancel()
			<-b.done
			b.cancel = nil
			b.log.Info("extension bridge stopped")
		}
		return
	}
	if b.cancel != nil {
		return
	}
	srv := bridge.New(bridge.Config{Port: b.cfg.Port, ExtensionIDs: b.cfg.IDs, Token: func() string { return b.token },
		OnTab: b.a.OnTab, Log: b.log})
	ctx, cancel := context.WithCancel(b.parent)
	b.cancel, b.done = cancel, make(chan struct{})
	done := b.done
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("extension bridge could not start (port in use?)", "addr", srv.Addr(), "err", err)
			return
		}
	}()
	b.log.Info("extension bridge listening", "addr", srv.Addr())
}
