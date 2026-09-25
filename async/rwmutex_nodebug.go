//go:build !velocitydebug

package async

// No-op outside debug builds, so the arguments are dead and the calls inline
// away. The consequence of a double release is the same silent exclusion break
// either way; what the tagged build adds is that it fails at the point of the
// mistake instead of in whatever runs next.
func checkReadRelease(int) {}

func checkWriteRelease(bool) {}
