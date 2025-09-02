package memorylocker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/lobicornis/v3/pkg/memorylocker"
)

func TestLocker_basic(t *testing.T) {
	locker := memorylocker.New()

	l, err := locker.Obtain(context.Background(), "key", time.Second)
	require.NoError(t, err)

	_, err = locker.Obtain(context.Background(), "key2", time.Second)
	assert.NoError(t, err)

	err = l.Release(context.Background())
	assert.NoError(t, err)
}

func TestLocker_deadlineExceeded(t *testing.T) {
	locker := memorylocker.New()

	_, err := locker.Obtain(context.Background(), "key", time.Second)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		_, err := locker.Obtain(context.Background(), "key", time.Second)
		assert.Error(t, err)
		assert.Equal(t, "can't obtain key: context deadline exceeded", err.Error())
		wg.Done()
	}()

	wg.Wait()
}

func TestLocker_acquireBeforeTTL(t *testing.T) {
	locker := memorylocker.New()

	l, err := locker.Obtain(context.Background(), "key", time.Second)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		_, err := locker.Obtain(context.Background(), "key", time.Second)
		assert.NoError(t, err)
		wg.Done()
	}()

	go func() {
		time.Sleep(500 * time.Millisecond)
		err := l.Release(context.Background())
		assert.NoError(t, err)
		wg.Done()
	}()

	go func() {
		time.Sleep(600 * time.Millisecond)
		_, err := locker.Obtain(context.Background(), "key", time.Second)
		assert.Error(t, err)
		assert.Equal(t, "can't obtain key: context deadline exceeded", err.Error())
		wg.Done()
	}()

	wg.Wait()
}

func TestLocker_Stress(t *testing.T) {
	timeout := time.After(2 * time.Second)

	concurrency := 10
	results := make(chan bool, concurrency)
	done := make(chan bool)
	go func() {
		locker := memorylocker.New()

		var wg sync.WaitGroup

		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			go func() {
				l, err := locker.Obtain(context.Background(), "key", 500*time.Millisecond)
				if err != nil {
					results <- false
				} else {
					time.Sleep(time.Second)

					err = l.Release(context.Background())
					assert.NoError(t, err)
					results <- true
				}

				wg.Done()
			}()
		}

		wg.Wait()
		close(results)

		locks := 0
		for res := range results {
			if res {
				locks++
			}
		}
		assert.Equal(t, 1, locks)

		done <- true
	}()

	select {
	case <-timeout:
		t.Fatal("Test didn't finish in time")
	case <-done:
	}
}
