// Package async provides typed, bounded concurrent task execution, collection
// fan-out, pipelines, ownership-aware fan-out, and tracked goroutine
// lifecycles.
//
// A Runner states the policy — a Limit and optional Hooks — once. Its
// Gather, Race, and FirstSuccess run distinct labeled tasks, one goroutine
// per task under the Limit; Map and ForEach run one function over a
// collection from a fixed pool of Limit goroutines. Results are always in
// source order.
package async
