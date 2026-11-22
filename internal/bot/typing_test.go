package bot

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeepTyping(t *testing.T) {
	var n atomic.Int32
	send := func(context.Context) { n.Add(1) }

	stop := keepTyping(context.Background(), send, 50*time.Millisecond, time.Hour)
	stop() // done before the first delay: nothing sent
	if n.Load() != 0 {
		t.Fatalf("fast reply sent %d typing actions", n.Load())
	}

	stop = keepTyping(context.Background(), send, 5*time.Millisecond, 20*time.Millisecond)
	time.Sleep(70 * time.Millisecond)
	stop()
	got := n.Load()
	if got < 2 || got > 5 {
		t.Fatalf("slow reply sent %d typing actions, want repeated", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n.Load() != got {
		t.Fatal("typing continued after stop")
	}
}
