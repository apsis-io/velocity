package resilience

import "context"

// UntilPolicy configures RetryUntil. It is a separate type from Policy
// because a condition has no retryable-error notion to classify: a probe
// reports whether the condition held, and an error from it is a reason to stop.
// Reusing Policy would have meant a Retryable field that RetryUntil silently
// ignored, which is a field set in good faith and obeyed by nobody.
type UntilPolicy struct {
	// MaxAttempts is the bound when positive. Zero means the caller's context
	// is the bound, which for a condition is usually the natural one: polling
	// until a deadline does not have a meaningful attempt count, and deriving
	// one is how a poll ends up quietly short of the budget it was given.
	MaxAttempts int
	Backoff     Backoff
	Clock       Clock
}

// RetryUntil calls probe until it reports the condition satisfied, the probe
// returns an error, or the bound ends the attempt loop.
//
// It is Retry for work that is "until this is true" rather than "until this
// succeeds" — a lock that is held, a socket that answers, a process that is
// gone. Those are the same loop, but Retry's error-shaped predicate inverts
// them: the probe has to return a sentinel error to mean "not yet", the
// classifier has to recognise that sentinel as retryable, and a probe with
// nothing to return has to invent a zero value to satisfy T. Here the
// condition is what the probe reports, and there is no sentinel to invert.
//
// A non-nil error from probe ends the loop and is returned unchanged: it is a
// failure, not "not yet". A probe that wants an error to be retried returns
// satisfied false with a nil error, which is the same as not being satisfied.
//
// The give-up is a *RetryError satisfying errors.Is(err, ErrGaveUp), as it is
// for Retry, so a caller asking whether its bound was reached has one answer
// whichever policy it used.
func RetryUntil[T any](ctx context.Context, policy UntilPolicy, probe func(context.Context) (T, bool, error)) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &PolicyError{Cause: context.Canceled}
	}

	if probe == nil {
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

		value, satisfied, err := probe(ctx)
		if err != nil {
			return zero, err
		}

		if satisfied {
			return value, nil
		}

		if policy.MaxAttempts > 0 && attempt == policy.MaxAttempts {
			return zero, &RetryError{Attempts: attempt}
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
