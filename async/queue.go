package async

import (
	"context"
	"errors"

	"github.com/apsis-io/nexty"
	"github.com/apsis-io/velocity/traits"
)

// ErrQueueClosed is returned when pushing to a closed Queue.
var ErrQueueClosed = errors.New("velocity/async: queue is closed")

// PopResult holds the outcome of a Queue pop: the popped item and whether an
// item was available (Ok is false only when the Queue is closed and drained).
type PopResult[T any] struct {
	Value T
	Ok    bool
}

type pushWaiter[T any] struct {
	val T
	fut *traits.Future[struct{}]
}

// Queue is a future-synchronized, bounded-or-unbounded FIFO queue.
//
// Neither producers nor consumers park on condition variables inside the
// queue: PushAsync and PopAsync return a *traits.Future that resolves as soon
// as the opposite side completes the handoff, and Push/Pop await those futures
// under the caller's context. Because Future.Complete is a single atomic
// compare-and-swap, a waiter whose context expires cancels its own Future via
// Complete; any producer or consumer that subsequently inspects that waiter
// observes Complete == false and skips it, so a cancelled wait never drops or
// duplicates an item.
type Queue[T any] struct {
	mu     nexty.Mutex
	cap    int
	buf    []T
	nextW  []*traits.Future[PopResult[T]]
	pushW  []pushWaiter[T]
	closed bool
}

// NewQueue returns a FIFO queue. A positive capacity bounds the buffer so
// pushes wait once len == capacity; a zero or negative capacity creates an
// unbounded queue whose pushes never wait.
func NewQueue[T any](capacity int) *Queue[T] {
	return &Queue[T]{cap: capacity}
}

// TryPush attempts to enqueue v without waiting. It reports whether v was
// accepted and whether the queue is closed.
func (q *Queue[T]) TryPush(v T) (accepted bool, closed bool) {
	q.mu.Lock()

	if q.closed {
		q.mu.Unlock()

		return false, true
	}

	if q.deliverToParkedConsumerLocked(v) {
		q.mu.Unlock()

		return true, false
	}

	if q.cap > 0 && len(q.buf) >= q.cap {
		q.mu.Unlock()

		return false, false
	}

	q.buf = append(q.buf, v)
	q.mu.Unlock()

	return true, false
}

// PushAsync enqueues v, returning a Future that resolves when v is accepted into
// the queue (or fails with ErrQueueClosed if the queue is closed).
func (q *Queue[T]) PushAsync(v T) *traits.Future[struct{}] {
	q.mu.Lock()

	if q.closed {
		q.mu.Unlock()

		return traits.NewResolvedFuture(struct{}{}, ErrQueueClosed)
	}

	if q.deliverToParkedConsumerLocked(v) {
		q.mu.Unlock()

		return traits.NewResolvedFuture(struct{}{}, nil)
	}

	if q.cap > 0 && len(q.buf) >= q.cap {
		fut := traits.NewFuture[struct{}]()
		q.pushW = append(q.pushW, pushWaiter[T]{val: v, fut: fut})
		q.mu.Unlock()

		return fut
	}

	q.buf = append(q.buf, v)
	q.mu.Unlock()

	return traits.NewResolvedFuture(struct{}{}, nil)
}

// Push enqueues v, waiting under ctx if a bounded queue is full.
// If ctx ends before v is accepted, Push cancels its waiter so v is never
// enqueued after Push returns an error.
func (q *Queue[T]) Push(ctx context.Context, v T) error {
	if ctx == nil {
		return ErrNilContext
	}

	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}

	fut := q.PushAsync(v)
	if res, ready := fut.Try(); ready {
		return res.Err
	}

	select {
	case <-fut.Done():
		res, _ := fut.Try()

		return res.Err

	case <-ctx.Done():
		cause := context.Cause(ctx)
		if fut.Complete(struct{}{}, cause) {
			return cause
		}

		// A consumer concurrently claimed our pushWaiter; wait for publication.
		<-fut.Done()
		res, _ := fut.Try()

		return res.Err
	}
}

// TryPop attempts to dequeue the oldest item without waiting. It returns
// (value, true, false) when an item was popped, (zero, false, true) when the
// queue is closed and empty, or (zero, false, false) when open and empty.
func (q *Queue[T]) TryPop() (value T, ok bool, closed bool) {
	q.mu.Lock()

	if len(q.buf) > 0 {
		v := q.buf[0]
		var zero T
		q.buf[0] = zero
		q.buf = q.buf[1:]
		q.admitParkedProducerLocked()
		q.mu.Unlock()

		return v, true, false
	}

	for len(q.pushW) > 0 {
		pw := q.pushW[0]
		q.pushW = q.pushW[1:]
		if pw.fut.Complete(struct{}{}, nil) {
			q.mu.Unlock()

			return pw.val, true, false
		}
	}

	isClosed := q.closed
	q.mu.Unlock()

	var zero T

	return zero, false, isClosed
}

// PopAsync returns a Future that resolves with the next item in FIFO order, or
// with PopResult{Ok: false} once the queue is closed and drained.
func (q *Queue[T]) PopAsync() *traits.Future[PopResult[T]] {
	q.mu.Lock()

	if len(q.buf) > 0 {
		v := q.buf[0]
		var zero T
		q.buf[0] = zero
		q.buf = q.buf[1:]
		q.admitParkedProducerLocked()
		q.mu.Unlock()

		return traits.NewResolvedFuture(PopResult[T]{Value: v, Ok: true}, nil)
	}

	for len(q.pushW) > 0 {
		pw := q.pushW[0]
		q.pushW = q.pushW[1:]
		if pw.fut.Complete(struct{}{}, nil) {
			q.mu.Unlock()

			return traits.NewResolvedFuture(PopResult[T]{Value: pw.val, Ok: true}, nil)
		}
	}

	if q.closed {
		q.mu.Unlock()

		return traits.NewResolvedFuture(PopResult[T]{Ok: false}, nil)
	}

	fut := traits.NewFuture[PopResult[T]]()
	q.nextW = append(q.nextW, fut)
	q.mu.Unlock()

	return fut
}

// Pop dequeues the next item in FIFO order, waiting under ctx until an item
// arrives or the queue is closed and drained. If ctx ends before an item is
// handed to this Pop, the waiter cancels its Future so no item is lost.
func (q *Queue[T]) Pop(ctx context.Context) (T, bool, error) {
	var zero T

	if ctx == nil {
		return zero, false, ErrNilContext
	}

	if err := ctx.Err(); err != nil {
		return zero, false, context.Cause(ctx)
	}

	fut := q.PopAsync()
	if res, ready := fut.Try(); ready {
		return res.Value.Value, res.Value.Ok, res.Err
	}

	select {
	case <-fut.Done():
		res, _ := fut.Try()

		return res.Value.Value, res.Value.Ok, res.Err

	case <-ctx.Done():
		cause := context.Cause(ctx)
		if fut.Complete(PopResult[T]{}, cause) {
			return zero, false, cause
		}

		// A producer already won Complete on fut and handed us an item; wait
		// for publication and return the item so it is never dropped.
		<-fut.Done()
		res, _ := fut.Try()

		return res.Value.Value, res.Value.Ok, res.Err
	}
}

// Close marks the queue as closed: parked producers fail with ErrQueueClosed,
// and parked consumers drain any buffered items before observing Ok == false.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()

		return
	}

	q.closed = true

	for _, pw := range q.pushW {
		pw.fut.Complete(struct{}{}, ErrQueueClosed)
	}

	q.pushW = nil

	for _, w := range q.nextW {
		if len(q.buf) > 0 {
			v := q.buf[0]
			if w.Complete(PopResult[T]{Value: v, Ok: true}, nil) {
				var zero T
				q.buf[0] = zero
				q.buf = q.buf[1:]
			}

			continue
		}

		w.Complete(PopResult[T]{Ok: false}, nil)
	}

	q.nextW = nil
	q.mu.Unlock()
}

// Len returns the current number of buffered items.
func (q *Queue[T]) Len() int {
	q.mu.Lock()
	n := len(q.buf)
	q.mu.Unlock()

	return n
}

// Closed reports whether Close has been called.
func (q *Queue[T]) Closed() bool {
	q.mu.Lock()
	closed := q.closed
	q.mu.Unlock()

	return closed
}

func (q *Queue[T]) deliverToParkedConsumerLocked(v T) bool {
	for len(q.nextW) > 0 {
		w := q.nextW[0]
		q.nextW[0] = nil
		q.nextW = q.nextW[1:]
		if w.Complete(PopResult[T]{Value: v, Ok: true}, nil) {
			return true
		}
	}

	return false
}

func (q *Queue[T]) admitParkedProducerLocked() {
	if q.cap <= 0 {
		return
	}

	for len(q.pushW) > 0 && len(q.buf) < q.cap {
		pw := q.pushW[0]
		q.pushW[0] = pushWaiter[T]{}
		q.pushW = q.pushW[1:]
		if pw.fut.Complete(struct{}{}, nil) {
			q.buf = append(q.buf, pw.val)

			return
		}
	}
}
