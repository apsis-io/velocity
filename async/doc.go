// Package async provides typed, bounded concurrent task execution, collection
// fan-out, pipelines, ownership-aware fan-out, and tracked goroutine
// lifecycles.
//
// Gather, Race, and FirstSuccess run a Plan of distinct labeled tasks, one
// goroutine per task under a Limit. Map and ForEach run one function over a
// collection from a fixed pool of Limit goroutines. Both report per-item
// Outcomes in source order.
package async
