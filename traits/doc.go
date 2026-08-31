// Package traits defines small, composable policies for copying and releasing
// values.
//
// Trait callbacks must return normally. They must not panic or call
// runtime.Goexit; errors are their supported failure channel.
package traits
