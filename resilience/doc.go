// Package resilience provides context-aware retry policies with classifiers,
// cancellation-aware backoff, hedged execution, circuit breakers, and
// injectable clocks.
//
// The three policies answer different failures. Retry handles an operation
// that failed; Hedge handles one that is merely slow, by starting another
// before the first has finished; Breaker handles a dependency that is
// failing enough that calling it again is the wrong move.
//
// Retry has a second form for work that is "until this is true" rather than
// "until this succeeds": RetryUntil, whose probe reports whether a condition
// held. Polling until a deadline has no meaningful attempt count, and deriving
// one is how a poll ends up quietly short of its own budget, so RetryUntil
// takes its bound from the context. Both forms report a give-up as a
// *RetryError satisfying errors.Is(err, ErrGaveUp), whichever bound ended the
// loop.
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
