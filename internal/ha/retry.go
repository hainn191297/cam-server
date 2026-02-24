package ha

import (
	"context"
	"math"
	"math/rand"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type RetryPolicy struct {
	Attempts       int
	InitialDelay   time.Duration
	MaxDelay       time.Duration
	Multiplier     float64
	JitterFraction float64
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	out := p
	if out.Attempts <= 0 {
		out.Attempts = 3
	}
	if out.InitialDelay <= 0 {
		out.InitialDelay = 150 * time.Millisecond
	}
	if out.MaxDelay <= 0 {
		out.MaxDelay = 1200 * time.Millisecond
	}
	if out.Multiplier <= 1 {
		out.Multiplier = 2
	}
	if out.JitterFraction < 0 {
		out.JitterFraction = 0
	}
	if out.JitterFraction > 1 {
		out.JitterFraction = 1
	}
	return out
}

func Retry(ctx context.Context, policy RetryPolicy, fn func(context.Context) error) error {
	policy = policy.withDefaults()
	var lastErr error

	for attempt := 1; attempt <= policy.Attempts; attempt++ {
		if err := fn(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == policy.Attempts {
			break
		}

		delay := policy.delayForAttempt(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return lastErr
}

func (p RetryPolicy) delayForAttempt(attempt int) time.Duration {
	backoff := float64(p.InitialDelay) * math.Pow(p.Multiplier, float64(attempt-1))
	if backoff > float64(p.MaxDelay) {
		backoff = float64(p.MaxDelay)
	}
	if p.JitterFraction > 0 {
		jitter := (rand.Float64()*2 - 1) * p.JitterFraction
		backoff = backoff * (1 + jitter)
	}
	if backoff < 0 {
		backoff = 0
	}
	return time.Duration(backoff)
}
