package opusclip

import (
	"context"
	"sync"
	"time"
)

// limiter is a token bucket: burst tokens, one refilled every interval. The
// 30/min account limit is burst 30 refilled every 2 s; the 1 req/s schedule
// family is burst 1 refilled every second.
type limiter struct {
	mu       sync.Mutex
	burst    float64
	interval time.Duration
	tokens   float64
	last     time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

func newLimiter(burst int, interval time.Duration) *limiter {
	return &limiter{burst: float64(burst), interval: interval, tokens: float64(burst), now: time.Now, sleep: sleepCtx}
}

// wait blocks until a token is available, then consumes it.
func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	t := l.now()
	if l.last.IsZero() {
		l.last = t
	}
	l.tokens += float64(t.Sub(l.last)) / float64(l.interval)
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = t
	var delay time.Duration
	if l.tokens >= 1 {
		l.tokens--
	} else {
		delay = time.Duration((1 - l.tokens) * float64(l.interval))
		l.tokens = 0
		// Reserve: the token that accrues during the delay is ours, so move
		// last forward so a concurrent caller does not double-count it.
		l.last = t.Add(delay)
	}
	l.mu.Unlock()
	if delay > 0 {
		return l.sleep(ctx, delay)
	}
	return ctx.Err()
}
