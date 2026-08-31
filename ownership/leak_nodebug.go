//go:build !velocitydebug

package ownership

func logLeakedBorrow(uint64, borrowKind) {}
