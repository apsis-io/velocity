// Package resilience provides context-aware retry policies with classifiers,
// cancellation-aware backoff, circuit breakers, and injectable clocks.
//
// Retry and Breaker compose by nesting: run the breaker inside the retried
// function, and classify ErrOpen as not retryable so a tripped breaker ends
// the loop instead of consuming attempts. Both take a Clock, so tests drive
// time explicitly; neither waits except Retry's backoff sleep.
package resilience
