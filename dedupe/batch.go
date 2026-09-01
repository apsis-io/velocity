package dedupe

import "context"

// Result holds the value or error for one requested key.
type Result[V any] struct {
	Value V
	Err   error
}

// DoBatch executes one function for all requested keys and aligns the result map
// with the requested keys. It is a plain-value form like Do, and an owned group
// reports ErrOwnedResult for every key.
func (g *Group[K, V]) DoBatch(ctx context.Context, keys []K, fn func(context.Context, []K) (map[K]V, error)) map[K]Result[V] {
	g.ready()
	results := make(map[K]Result[V], len(keys))
	var err error
	switch {
	case g.owned:
		err = ErrOwnedResult
	case ctx == nil:
		err = ErrNilContext
	case fn == nil:
		err = ErrNilFunction
	}
	if err != nil {
		for _, key := range keys {
			results[key] = Result[V]{Err: err}
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
	for i, key := range unique {
		call, leader, err := g.joinWithExecution(ctx, key, exec)
		if err != nil {
			// Waiting for an abandoned round was cut short: undo the joins
			// made so far and report the cause for every key.
			for _, joined := range unique[:i] {
				g.leave(joined, calls[joined])
			}
			exec.cancel()
			for _, key := range keys {
				results[key] = Result[V]{Err: err}
			}
			return results
		}
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
		value, err := g.wait(ctx, key, calls[key])
		results[key] = Result[V]{Value: value, Err: err}
	}
	return results
}
