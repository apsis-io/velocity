package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apsis-io/velocity/resilience"
)

func TestRetryUntilStopsWhenTheConditionHolds(t *testing.T) {
	probes := 0

	got, err := resilience.RetryUntil(context.Background(), resilience.UntilPolicy{MaxAttempts: 5},
		func(context.Context) (string, bool, error) {
			probes++
			return "ready", probes == 3, nil
		})
	if err != nil || got != "ready" || probes != 3 {
		t.Fatalf("RetryUntil = (%q, %v) after %d probes", got, err, probes)
	}
}

func TestRetryUntilProbeErrorEndsTheLoop(t *testing.T) {
	boom := errors.New("boom")
	probes := 0
	_, err := resilience.RetryUntil(context.Background(), resilience.UntilPolicy{MaxAttempts: 5},
		func(context.Context) (int, bool, error) {
			probes++
			return 0, false, boom
		})
	// An error from a probe is a failure, not "not yet": it is returned
	// unchanged rather than retried or wrapped as a give-up.
	if !errors.Is(err, boom) || probes != 1 {
		t.Fatalf("RetryUntil = %v after %d probes", err, probes)
	}

	if errors.Is(err, resilience.ErrGaveUp) {
		t.Fatalf("a probe error was reported as a give-up: %v", err)
	}
}

// TestRetryUntilIsBoundedByItsContextNotByADerivedCount is the regression for
// the under-wait a required integer causes. Polling until a deadline has no
// meaningful attempt count; attempts = budget/interval binds first and ends the
// poll up to one interval short of the budget it was handed. With
// MaxAttempts at zero the deadline is the only bound, so the probe runs until
// the context ends it.
func TestRetryUntilIsBoundedByItsContextNotByADerivedCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	backoff, err := resilience.ExponentialBackoff(5*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}

	probes := 0

	_, err = resilience.RetryUntil(ctx, resilience.UntilPolicy{Backoff: backoff},
		func(context.Context) (struct{}, bool, error) {
			probes++
			return struct{}{}, false, nil
		})
	if !errors.Is(err, resilience.ErrGaveUp) {
		t.Fatalf("RetryUntil = %v, want a give-up", err)
	}

	if probes < 2 {
		t.Fatalf("probed %d times; the context, not an attempt count, should bound it", probes)
	}
}

func TestRetryUntilGivesUpOnItsAttemptBound(t *testing.T) {
	probes := 0
	_, err := resilience.RetryUntil(context.Background(), resilience.UntilPolicy{MaxAttempts: 3},
		func(context.Context) (int, bool, error) {
			probes++
			return 0, false, nil
		})

	var giveUp *resilience.RetryError
	if !errors.As(err, &giveUp) || giveUp.Attempts != 3 || probes != 3 {
		t.Fatalf("RetryUntil = %v after %d probes", err, probes)
	}

	if !errors.Is(err, resilience.ErrGaveUp) {
		t.Fatalf("RetryUntil = %v, want ErrGaveUp", err)
	}

	if giveUp.Last != nil {
		t.Fatalf("a condition that never held reported a last error: %v", giveUp.Last)
	}
}

func TestRetryUntilRejectsAnUnboundedPolicy(t *testing.T) {
	_, err := resilience.RetryUntil(context.Background(), resilience.UntilPolicy{},
		func(context.Context) (int, bool, error) { return 0, true, nil })
	if !errors.Is(err, resilience.ErrNoBound) {
		t.Fatalf("RetryUntil = %v, want ErrNoBound", err)
	}

	if !errors.Is(err, resilience.ErrInvalidPolicy) {
		t.Fatalf("RetryUntil = %v, want it to be a policy error", err)
	}
}

func TestRetryUntilValidation(t *testing.T) {
	_, err := resilience.RetryUntil[int](context.Background(), resilience.UntilPolicy{MaxAttempts: 1}, nil)
	if !errors.Is(err, resilience.ErrNilFunction) {
		t.Fatalf("nil probe = %v", err)
	}

	//lint:ignore SA1012 a nil context is what is under test
	_, err = resilience.RetryUntil(nil, resilience.UntilPolicy{MaxAttempts: 1},
		func(context.Context) (int, bool, error) { return 0, true, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("nil ctx = %v", err)
	}
}

// TestRetryUntilHonoursAConditionMetOnTheFinalAllowedProbe pins the order of
// the two checks at the bottom of the loop. A probe that ran and found the
// condition satisfied is a success, and the bound must not be consulted
// afterwards and turn it into a give-up: the natural implementation — check
// the bound after the probe, before believing it — would swallow exactly this,
// and the symptom would be a poll reporting "gave up" for a condition that was
// demonstrably met inside its own budget.
func TestRetryUntilHonoursAConditionMetOnTheFinalAllowedProbe(t *testing.T) {
	probes := 0

	got, err := resilience.RetryUntil(context.Background(), resilience.UntilPolicy{MaxAttempts: 3},
		func(context.Context) (string, bool, error) {
			probes++
			return "last", probes == 3, nil
		})
	if err != nil || got != "last" || probes != 3 {
		t.Fatalf("RetryUntil = (%q, %v) after %d probes", got, err, probes)
	}
}

// The same, one probe short of the bound, has to give up rather than run a
// fourth — the other half of the same ordering.
func TestRetryUntilGivesUpOneProbePastTheBound(t *testing.T) {
	probes := 0

	_, err := resilience.RetryUntil(context.Background(), resilience.UntilPolicy{MaxAttempts: 3},
		func(context.Context) (int, bool, error) {
			probes++
			return 0, probes == 4, nil
		})
	if !errors.Is(err, resilience.ErrGaveUp) || probes != 3 {
		t.Fatalf("RetryUntil = %v after %d probes", err, probes)
	}
}
