package ha

import (
	"context"
	"sync"
	"time"
)

type CachedResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

type idempotencyEntry struct {
	createdAt time.Time
	expiresAt time.Time
	done      chan struct{}
	ready     bool
	response  CachedResponse
	err       error
}

type IdempotencyStore struct {
	ttl     time.Duration
	maxKeys int
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]*idempotencyEntry
}

func NewIdempotencyStore(ttl time.Duration, maxKeys int) *IdempotencyStore {
	if ttl <= 0 {
		ttl = 3 * time.Minute
	}
	if maxKeys <= 0 {
		maxKeys = 20000
	}

	return &IdempotencyStore{
		ttl:     ttl,
		maxKeys: maxKeys,
		now:     time.Now,
		entries: make(map[string]*idempotencyEntry),
	}
}

// Do executes producer once for a key and replays the same response for
// duplicates within the configured TTL.
func (s *IdempotencyStore) Do(
	ctx context.Context,
	key string,
	producer func(context.Context) (CachedResponse, error),
) (CachedResponse, bool, error) {
	if key == "" {
		return s.runWithoutKey(ctx, producer)
	}

	for {
		entry, replay, waitCh, err := s.prepareEntry(ctx, key)
		if err != nil {
			return CachedResponse{}, false, err
		}
		if replay != nil {
			return *replay, true, entry.err
		}
		if waitCh != nil {
			if err := waitForEntry(ctx, waitCh); err != nil {
				return CachedResponse{}, false, err
			}
			continue
		}

		resp, err := producer(ctx)
		resp = cloneResponse(resp)
		s.finalizeEntry(key, entry, resp, err)
		return resp, false, err
	}
}

func (s *IdempotencyStore) runWithoutKey(
	ctx context.Context,
	producer func(context.Context) (CachedResponse, error),
) (CachedResponse, bool, error) {
	resp, err := producer(ctx)
	return cloneResponse(resp), false, err
}

func (s *IdempotencyStore) prepareEntry(
	_ context.Context,
	key string,
) (*idempotencyEntry, *CachedResponse, chan struct{}, error) {
	now := s.now()
	s.lock()
	defer s.unlock()

	s.gcLocked(now)

	if entry, ok := s.entries[key]; ok {
		if replay := replayResponse(entry, now); replay != nil {
			return entry, replay, nil, nil
		}
		if entry.ready {
			delete(s.entries, key)
		} else {
			return entry, nil, entry.done, nil
		}
	}

	entry := &idempotencyEntry{
		createdAt: now,
		expiresAt: now.Add(s.ttl),
		done:      make(chan struct{}),
	}
	s.entries[key] = entry
	s.enforceMaxKeysLocked()
	return entry, nil, nil, nil
}

func replayResponse(entry *idempotencyEntry, now time.Time) *CachedResponse {
	if !entry.ready || !now.Before(entry.expiresAt) {
		return nil
	}
	resp := cloneResponse(entry.response)
	return &resp
}

func waitForEntry(ctx context.Context, waitCh chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitCh:
		return nil
	}
}

func (s *IdempotencyStore) finalizeEntry(
	key string,
	entry *idempotencyEntry,
	resp CachedResponse,
	err error,
) {
	s.lock()
	defer s.unlock()

	latest, ok := s.entries[key]
	if !ok || latest != entry {
		return
	}

	if err != nil {
		delete(s.entries, key)
	} else {
		entry.response = resp
		entry.expiresAt = s.now().Add(s.ttl)
	}
	entry.err = err
	entry.ready = true
	close(entry.done)
}

func cloneResponse(resp CachedResponse) CachedResponse {
	out := resp
	if len(resp.Body) > 0 {
		out.Body = append([]byte(nil), resp.Body...)
	}
	return out
}

func (s *IdempotencyStore) gcLocked(now time.Time) {
	for k, entry := range s.entries {
		if entry.ready && !now.Before(entry.expiresAt) {
			delete(s.entries, k)
		}
	}
}

func (s *IdempotencyStore) enforceMaxKeysLocked() {
	if len(s.entries) <= s.maxKeys {
		return
	}

	for len(s.entries) > s.maxKeys {
		var victimKey string
		var victim *idempotencyEntry

		for k, entry := range s.entries {
			if !entry.ready {
				continue
			}
			if victim == nil || entry.createdAt.Before(victim.createdAt) {
				victimKey = k
				victim = entry
			}
		}
		if victim == nil {
			break
		}
		delete(s.entries, victimKey)
	}
}

func (s *IdempotencyStore) lock() {
	s.mu.Lock()
}

func (s *IdempotencyStore) unlock() {
	s.mu.Unlock()
}
