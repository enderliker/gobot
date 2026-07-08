package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
)

var (
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	active atomic.Int64
)

func Init() {
	mu.Lock()
	defer mu.Unlock()

	if cancel != nil {
		cancel()
	}
	ctx, cancel = context.WithCancel(context.Background())
	active.Store(0)
}

func Context() context.Context {
	mu.RLock()
	defer mu.RUnlock()

	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func Go(fn func(context.Context)) {
	wg.Add(1)
	active.Add(1)
	go func() {
		defer func() {
			active.Add(-1)
			wg.Done()
		}()
		fn(Context())
	}()
}

func IsShuttingDown() bool {
	select {
	case <-Context().Done():
		return true
	default:
		return false
	}
}

func Cancel() {
	mu.RLock()
	currentCancel := cancel
	mu.RUnlock()

	if currentCancel != nil {
		currentCancel()
	}
}

func Wait() {
	wg.Wait()
}

func ActiveTasks() int64 {
	return active.Load()
}
