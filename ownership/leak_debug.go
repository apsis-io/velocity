//go:build velocitydebug

package ownership

import (
	"log/slog"
	"runtime"
)

// trackLeak registers a cleanup that fires once borrow is unreachable while
// its lease is still held: a leaked advanced borrow. Debug builds only.
func trackLeak[B, T any](borrow *B, lease *lease[T]) runtime.Cleanup {
	return runtime.AddCleanup(borrow, cleanupLease[T], lease)
}

func cleanupLease[T any](lease *lease[T]) {
	released, _ := lease.release(OpRelease)
	if released {
		logLeakedBorrow(lease.id, lease.kind)
	}
}

func logLeakedBorrow(id uint64, kind borrowKind) {
	kindName := "read"
	if kind == borrowWrite {
		kindName = "write"
	}
	slog.Warn("velocity ownership borrow leaked", "borrow_id", id, "kind", kindName)
}
