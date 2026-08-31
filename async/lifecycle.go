package async

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
)

// Panic captures a callback panic and its originating goroutine stack.
type Panic struct {
	Value any
	Stack []byte
}

func (p *Panic) Error() string { return fmt.Sprintf("panic: %v\n%s", p.Value, p.Stack) }
func (p *Panic) Unwrap() error {
	if err, ok := p.Value.(error); ok {
		return err
	}
	return nil
}

// Group owns callback goroutines and propagates the first panic from Wait.
type Group struct {
	mu sync.Mutex
	wg sync.WaitGroup

	active  int
	closing bool
	done    chan struct{}
	panic   *Panic
}

// Go starts f unless the group is closing or closed.
func (g *Group) Go(f func()) error {
	if f == nil {
		return ErrNilTask
	}
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return ErrClosed
	}
	if g.done == nil {
		g.done = make(chan struct{})
	}
	g.active++
	g.wg.Go(func() {
		defer g.finish()
		defer func() {
			if value := recover(); value != nil {
				g.capture(value)
			}
		}()
		f()
	})
	g.mu.Unlock()
	return nil
}

func (g *Group) capture(value any) {
	g.mu.Lock()
	if g.panic == nil {
		g.panic = &Panic{Value: value, Stack: debug.Stack()}
	}
	g.mu.Unlock()
}

func (g *Group) finish() {
	g.mu.Lock()
	g.active--
	if g.closing && g.active == 0 {
		close(g.done)
	}
	g.mu.Unlock()
}

// Wait blocks for every callback and re-panics with the first captured Panic.
func (g *Group) Wait() {
	g.wg.Wait()
	g.mu.Lock()
	panicValue := g.panic
	g.mu.Unlock()
	if panicValue != nil {
		panic(panicValue)
	}
}

// Close stops new callbacks and waits for active callbacks through one shared
// terminal channel. A timed-out Close leaves the group closing.
func (g *Group) Close(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	g.mu.Lock()
	if g.done == nil {
		g.done = make(chan struct{})
	}
	g.closing = true
	if g.active == 0 {
		select {
		case <-g.done:
		default:
			close(g.done)
		}
	}
	done := g.done
	g.mu.Unlock()
	select {
	case <-done:
		g.Wait()
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
