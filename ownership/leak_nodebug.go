//go:build !velocitydebug

package ownership

import "runtime"

// trackLeak is a no-op outside debug builds: a zero runtime.Cleanup tolerates
// Stop, so Release needs no special case.
func trackLeak[B, T any](*B, *lease[T]) runtime.Cleanup { return runtime.Cleanup{} }
