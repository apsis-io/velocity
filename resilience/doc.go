// Package resilience provides context-aware retry policies with classifiers,
// cancellation-aware backoff, hedged execution, circuit breakers, and
// injectable clocks.
//
// The three policies answer different failures. Retry handles an operation
// that failed; Hedge handles one that is merely slow, by starting another
// before the first has finished; Breaker handles a dependency that is
// failing enough that calling it again is the wrong move.
//
// Retry and Breaker compose by nesting: run the breaker inside the retried
// function, and classify ErrOpen as not retryable so a tripped breaker ends
// the loop instead of consuming attempts. Both take a Clock, so tests drive
// time explicitly; neither waits except Retry's backoff sleep.
package resilience
