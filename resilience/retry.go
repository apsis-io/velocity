package resilience

import (
	"context"
)

// Policy configures Retry.
//
// MaxAttempts is the bound when positive. Zero means the caller's context is
// the bound, which is the right shape for work that has a deadline of its own:
// attempts = budget/interval is a derivation that binds first, ends the loop up
// to one interval short of the budget, and does so silently. A policy that
// bounds nothing — zero attempts and a context with no deadline — is
// ErrInvalidPolicy.
type Policy struct {
	MaxAttempts int
	Retryable   Classifier
	Backoff     Backoff
	Clock       Clock
}

// Retry runs fn until it succeeds, a non-retryable error, the attempt limit,
// or context cancellation.
//
// Two of those are decisions and the rest are give-ups, and the distinction is
// the error a caller receives:
//
//   - A non-retryable error is returned unchanged. The policy chose to stop, so
//     it is an answer rather than running out of options, and it does not
//     satisfy ErrGaveUp.
//   - The attempt limit, a context that ended before an attempt could start,
//     and a backoff sleep a context interrupted are all give-ups: the policy ran
//     out of bound rather than out of reasons. Each reports a *RetryError
//     satisfying errors.Is(err, ErrGaveUp), so the one question a caller has —
//     did my bound stop this — has one answer whichever bound ended the loop.
//
// The last two used to return the bare context cause, which meant the error type
// a caller received depended on whether the attempt count or the deadline
// happened to bind first — and therefore on arithmetic the caller wrote rather
// than on the situation.
func Retry[T any](ctx context.Context, policy Policy, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &PolicyError{Cause: context.Canceled}
	}

	if fn == nil {
		return zero, &PolicyError{Cause: ErrNilFunction}
	}

	if err := validBound(ctx, policy.MaxAttempts); err != nil {
		return zero, err
	}

	clock := policy.Clock
	if clock == nil {
		clock = RealClock()
	}

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, &RetryError{Attempts: attempt - 1, Last: context.Cause(ctx)}
		}

		value, err := fn(ctx)
		if err == nil {
			return value, nil
		}

		if policy.Retryable != nil && !policy.Retryable(err) {
			return zero, err
		}

		if policy.MaxAttempts > 0 && attempt == policy.MaxAttempts {
			return zero, &RetryError{Attempts: attempt, Last: err}
		}

		if policy.Backoff == nil {
			continue
		}

		delay := policy.Backoff(attempt)
		if err := clock.Sleep(ctx, delay); err != nil {
			return zero, &RetryError{Attempts: attempt, Last: err}
		}
	}
}

// validBound rejects a policy that bounds nothing. It runs at first use rather
// than at construction because the context is an argument and Policy does not
// carry one — which is also the moment the information is actionable, since it
// is when a caller discovers their bound never bounded anything.
func validBound(ctx context.Context, maxAttempts int) error {
	if maxAttempts < 0 {
		return &PolicyError{Cause: ErrInvalidPolicy}
	}

	if maxAttempts > 0 {
		return nil
	}

	if _, ok := ctx.Deadline(); !ok {
		return &PolicyError{Cause: ErrNoBound}
	}

	return nil
}
