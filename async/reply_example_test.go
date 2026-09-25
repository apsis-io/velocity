package async_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/apsis-io/velocity/async"
)

// replyRegistry is the whole of a request/reply exchange: a correlation key, a
// channel to deliver on, and a removal the responder performs.
//
// It is not part of the package. It is here because a measurement decided the
// shape — the obvious way to write this leaks two goroutines per caller that
// abandons its reply, and moving the cleanup to the responder takes that to
// zero — and because a shape that only ever appears in prose gets written the
// leaking way the second time.
type replyRegistry[V any] struct {
	mu      sync.Mutex
	waiters map[string]chan V
}

func newReplyRegistry[V any]() *replyRegistry[V] {
	return &replyRegistry[V]{waiters: make(map[string]chan V)}
}

// listen registers a waiter and returns it with the function that removes it.
func (r *replyRegistry[V]) listen(key string) (<-chan V, func()) {
	replies := make(chan V, 1)

	r.mu.Lock()
	r.waiters[key] = replies
	r.mu.Unlock()

	var once sync.Once

	return replies, func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.waiters, key)
			r.mu.Unlock()
			close(replies)
		})
	}
}

// send never blocks the responder. A second reply to a caller that has already
// read one is dropped rather than parked on a full channel, because a channel
// nobody is reading is how a goroutine gets stranded forever: the responder
// would block on a send that is never received, so the code after it — including
// the remove that would have cleaned up — would never run.
func (r *replyRegistry[V]) send(key string, v V) {
	r.mu.Lock()
	replies, ok := r.waiters[key]
	r.mu.Unlock()

	if !ok {
		return
	}

	select {
	case replies <- v:
	default:
	}
}

func (r *replyRegistry[V]) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.waiters)
}

// A caller reads one reply and walks away, which is what a request looks like
// when its answer arrives after the caller stopped caring. The caller creates
// the entry and then hands the removal to the responder, so nothing it does
// afterwards — including forgetting, which is the whole case — can strand the
// responder. The responder's own defer is what cleans up, and a defer runs.
func Example_requestReply() {
	registry := newReplyRegistry[string]()

	// The caller creates the entry, then gives the removal away.
	replies, remove := registry.listen("order-1")

	// The responder: fan out two replies, then clean up on the way out.
	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		defer remove()

		registry.send("order-1", "accepted")
		registry.send("order-1", "duplicate") // nobody is listening; dropped
	}()

	// The caller: one reply, then gone.
	fmt.Println("caller read:", <-replies)
	<-responderDone
	fmt.Println("waiters left:", registry.size())
	// Output:
	// caller read: accepted
	// waiters left: 0
}

// The same exchange under a bound. The responder runs through an ErrGroup, so
// the number in flight is the Runner's limit rather than however many callers
// happen to have shown up.
//
// Two things here are the shape rather than the plumbing, and both are about
// not stranding a goroutine:
//
//   - A submitter that must be able to stop uses GoCtx. A loop calling Go
//     blocks on the permit with a plain send, so with every permit held it
//     cannot reach its own cancellation branch — and a loop that cannot reach
//     its cancel is a loop that can never shut down. GoCtx selects on the
//     context instead, so cancelling from elsewhere releases it.
//   - The group is shut down at the end rather than abandoned. A responder
//     parked on its context is a goroutine the example still owns, and leaving
//     one behind does not merely leak: it hangs the test harness, which waits
//     for the example's output to close.
func Example_requestReply_bounded() {
	ctx, cancel := context.WithCancel(context.Background())

	run, _ := async.New(async.Limited(2))
	eg, _ := run.ErrGroup(ctx)
	registry := newReplyRegistry[string]()

	// Each responder reports that it has registered, so "admitted" is observed
	// rather than raced: Go returns as soon as the goroutine is started, not
	// when the function has run.
	registered := make(chan struct{}, 2)
	responder := func(key string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, remove := registry.listen(key)
			defer remove()

			registry.send(key, "accepted")

			registered <- struct{}{}

			<-ctx.Done() // the caller is still reading

			return nil
		}
	}

	// The first two are admitted synchronously and take both permits.
	eg.Go(responder("order-1"))
	eg.Go(responder("order-2"))
	<-registered
	<-registered
	fmt.Println("admitted:", registry.size())

	// The third cannot be admitted, so it waits — on its own goroutine, since a
	// submit loop that blocks here could not reach the cancel below.
	waiting := make(chan struct{})
	go func() {
		defer close(waiting)

		eg.GoCtx(ctx, responder("order-3"))
	}()

	// Shutting down releases both ends at once: the waiting submitter's select
	// takes the cancellation and the function never runs, and the two admitted
	// responders return because their context is done.
	cancel()
	<-waiting
	fmt.Println("after shutdown:", eg.Wait() == nil, "waiters:", registry.size())
	// Output:
	// admitted: 2
	// after shutdown: true waiters: 0
}

// The measured property, as a test. Twenty-five callers each read one reply of
// two and walk away — the shape of a request whose answer arrives after the
// caller stopped caring. The obvious implementation strands a goroutine per
// abandoned call, because the responder blocks sending a reply nobody is
// reading and so never reaches the code that would clean up; that measured as
// +50 goroutines over 25 calls.
//
// Nothing here observes a goroutine count, so the assertion is about the thing
// that actually matters: every responder returned, and the registry is empty.
func TestRequestReplyAbandonmentLeavesNothingBehind(t *testing.T) {
	const calls = 25

	registry := newReplyRegistry[string]()
	done := make(chan struct{}, calls)

	for i := range calls {
		key := strconv.Itoa(i)
		// The caller registers, then abandons without reading anything.
		_, remove := registry.listen(key)

		go func() {
			defer func() { done <- struct{}{} }()
			defer remove()
			// A fan-out responder: more replies than the caller will ever read.
			// The second send is dropped rather than parked on a full channel.
			registry.send(key, "accepted")
			registry.send(key, "duplicate")
		}()
	}

	for range calls {
		<-done
	}

	if got := registry.size(); got != 0 {
		t.Fatalf("%d waiters left registered after %d abandoned calls", got, calls)
	}
}
