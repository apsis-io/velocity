//go:build velocitydebug

package ownership

import "log/slog"

func logLeakedBorrow(id uint64, kind borrowKind) {
	kindName := "read"
	if kind == borrowWrite {
		kindName = "write"
	}
	slog.Warn("velocity ownership borrow leaked", "borrow_id", id, "kind", kindName)
}
