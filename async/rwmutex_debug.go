//go:build velocitydebug

package async

// The unlock paths have no floor: `readers--` and `held = false` trust that a
// permit is released exactly once, and nothing checks. A double release drives
// the reader count negative, which makes `readers == 0` unreachable — so
// `wake` never fires, waiting readers never re-read the state, and a writer is
// admitted while readers still hold the lock. That is a silent break of the
// exclusion this type exists to provide, and it is the worst shape a
// concurrency bug can take: no error, no panic, and the symptom appearing in
// whatever runs next.
//
// Permit.Release absorbs the ordinary case — a double call is a no-op, because
// it is guarded by a sync.Once. These checks are the net for what the guard does
// not cover: a release that reaches the counter by another route, and a permit
// released while nothing is held. Under -tags=velocitydebug they turn both into
// a panic at the point of the mistake, which is the build in which anyone is
// reading a stack trace.
func checkReadRelease(readers int) {
	if readers < 0 {
		panic("async: a read permit was released more than once")
	}
}

func checkWriteRelease(held bool) {
	if !held {
		panic("async: a write permit was released while none was held")
	}
}
