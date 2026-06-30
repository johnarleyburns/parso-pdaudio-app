package pipeline

import (
	"context"
	"sync"
	"time"
)

// stepFunc performs one unit of work. It returns true if it claimed and
// processed a row, false if there was no work available.
type stepFunc func(ctx context.Context, workerID string) bool

// pool manages a dynamically sized set of worker goroutines all running the
// same step function. The work uses the engine context; per-worker contexts
// only control how many goroutines are alive (scaling).
type pool struct {
	name   string
	step   stepFunc
	engCtx context.Context

	mu      sync.Mutex
	cancels []context.CancelFunc
	wg      sync.WaitGroup
}

func newPool(engCtx context.Context, name string, step stepFunc) *pool {
	return &pool{name: name, step: step, engCtx: engCtx}
}

// Size returns the current number of live workers.
func (p *pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cancels)
}

// Scale grows or shrinks the pool to exactly n workers (n >= 0).
func (p *pool) Scale(n int) {
	if n < 0 {
		n = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.cancels) < n {
		ctx, cancel := context.WithCancel(p.engCtx)
		p.cancels = append(p.cancels, cancel)
		id := p.name + "-" + itoa(len(p.cancels))
		p.wg.Add(1)
		go p.run(ctx, id)
	}
	for len(p.cancels) > n {
		last := len(p.cancels) - 1
		p.cancels[last]()
		p.cancels = p.cancels[:last]
	}
}

func (p *pool) run(ctx context.Context, id string) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		did := p.step(p.engCtx, id)
		if did {
			continue
		}
		// No work: park briefly, but wake on shrink/cancel.
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Stop cancels all workers and waits for them to exit.
func (p *pool) Stop() {
	p.Scale(0)
	p.wg.Wait()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
