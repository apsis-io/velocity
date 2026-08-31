package async

import "context"

// Pipeline composes heterogeneous stages while retaining static result types.
// It is this package's equivalent of Hunch's Waterfall, expressed as a
// fluent chain (Start(fn).Then(...).Then(...).Run(ctx)) rather than a flat
// variadic call, since Go can't express Waterfall's original
// same-type-throughout variadic signature and still let each stage change
// type the way Then does.
type Pipeline[T any] struct {
	run func(context.Context) (T, error)
}

// Start creates a pipeline with its first stage.
func Start[T any](fn func(context.Context) (T, error)) Pipeline[T] {
	return Pipeline[T]{run: fn}
}

// Then appends a stage that transforms the preceding stage's result.
func (p Pipeline[T]) Then[R any](fn func(context.Context, T) (R, error)) Pipeline[R] {
	if fn == nil {
		return Pipeline[R]{}
	}
	return Pipeline[R]{run: func(ctx context.Context) (R, error) {
		value, err := p.Run(ctx)
		if err != nil {
			var zero R
			return zero, err
		}
		return fn(ctx, value)
	}}
}

// Run executes the pipeline in order and stops at the first error.
func (p Pipeline[T]) Run(ctx context.Context) (T, error) {
	if p.run == nil {
		var zero T
		return zero, &PipelineError{Cause: ErrNilPipeline}
	}
	if ctx == nil {
		var zero T
		return zero, &PipelineError{Cause: ErrNilContext}
	}
	return p.run(ctx)
}
