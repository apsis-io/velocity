package dedupe

import (
	"context"

	"github.com/apsis-io/velocity/ownership"
)

// Result holds the independent result handle for one requested key.
type Result[V any] struct {
	Handle *ownership.Shared[V]
	Err    error
}

// DoBatch executes one function for all requested keys and aligns the result map
// with the requested keys.
func (g *Group[K, V]) DoBatch(ctx context.Context, keys []K, fn func(context.Context, []K) (map[K]V, error)) map[K]Result[V] {
	results := make(map[K]Result[V], len(keys))
	if ctx == nil {
		for _, key := range keys {
			results[key] = Result[V]{Err: ErrNilContext}
		}
		return results
	}
	if fn == nil {
		for _, key := range keys {
			results[key] = Result[V]{Err: ErrNilFunction}
		}
		return results
	}
	unique := make([]K, 0, len(keys))
	seen := make(map[K]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			unique = append(unique, key)
		}
	}
	leaders := make([]K, 0, len(unique))
	calls := make(map[K]*call[V], len(unique))
	exec := newExecution(g.baseCtx)
	for _, key := range unique {
		call, leader := g.joinWithExecution(key, exec)
		calls[key] = call
		if leader {
			leaders = append(leaders, key)
		}
	}
	if len(leaders) == 0 {
		exec.cancel()
	}
	if len(leaders) > 0 {
		go g.runBatch(leaders, calls, fn)
	}
	for _, key := range unique {
		handle, err := g.wait(ctx, key, calls[key])
		results[key] = Result[V]{Handle: handle, Err: err}
	}
	return results
}
