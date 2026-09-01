// Package resilience provides context-aware retry policies with classifiers,
// cancellation-aware backoff, hedged execution, circuit breakers, and
// injectable clocks.
//
// The three policies answer different failures. Retry handles an operation
// that failed; Hedge handles one that is merely slow, by starting another
// before the first has finished; Breaker handles a dependency that is
// failing enough that calling it again is the wrong move.
//
// The package is deliberately three policies, not a resilience framework.
// failsafe-go covers far more — timeout, fallback, rate limiting, bulkhead,
// adaptive concurrency and throttling, HTTP and gRPC integrations — is
// actively maintained, and should be preferred when breadth is what is
// wanted. What it cannot express is that a result may own something: every
// policy that discards a result leaks it when the result is a connection or
// a file. Hedge's Discard closes that hole here, and the separate
// failsafeown module closes it over failsafe-go's own policies.
//
// Retry and Breaker compose by nesting: run the breaker inside the retried
// function, and classify ErrOpen as not retryable so a tripped breaker ends
// the loop instead of consuming attempts. Both take a Clock, so tests drive
// time explicitly; neither waits except Retry's backoff sleep.
package resilience
