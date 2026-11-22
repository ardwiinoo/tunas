package bot

import (
	"context"
	"sync"
	"time"
)

// Typing indicator timing. Telegram shows "typing..." for about 5 seconds
// per sendChatAction, so it is repeated while the reply is being prepared.
// The first one waits briefly so instant replies (most commands) send none.
const (
	typingFirst = 300 * time.Millisecond
	typingEvery = 4 * time.Second
)

// keepTyping calls send after first and then every interval until the
// returned stop is called. stop waits for an in-flight send to finish.
func keepTyping(ctx context.Context, send func(context.Context), first, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTimer(first)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				send(ctx)
				t.Reset(every)
			}
		}
	})
	return func() { cancel(); wg.Wait() }
}
