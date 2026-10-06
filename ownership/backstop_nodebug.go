//go:build !velocitydebug

package ownership

// logNetDrop is silent outside debug builds. See backstop_debug.go.
func logNetDrop() {}
