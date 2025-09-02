package memorylocker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/traefik/lobicornis/v3/pkg/locker"
)

type Locker struct {
	mu    *sync.Mutex
	locks map[string]*Lock
}

func New() *Locker {
	return &Locker{
		mu:    &sync.Mutex{},
		locks: make(map[string]*Lock),
	}
}

func (l Locker) Obtain(ctx context.Context, key string, ttl time.Duration) (locker.Lock, error) {
	l.mu.Lock()

	lock, hasKey := l.locks[key]
	if !hasKey {
		lock = &Lock{
			mu: &sync.Mutex{},
		}

		lock.mu.Lock()
		l.locks[key] = lock
		l.mu.Unlock()

		return lock, nil
	}
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("can't obtain %s: %w", key, ctx.Err())
		case <-ticker.C:
			if lock.mu.TryLock() {
				ticker.Stop()
				return lock, nil
			}
		}
	}
}

type Lock struct {
	mu *sync.Mutex
}

func (l *Lock) Release(ctx context.Context) error {
	if l.mu != nil {
		l.mu.Unlock()
	}
	return nil
}
