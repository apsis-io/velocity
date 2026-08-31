package dedupe

import (
	"hash/maphash"
	"sync"

	"github.com/puzpuzpuz/xsync/v4"
)

type backend[K comparable, V any] interface {
	loadOrStore(K, *call[V]) (*call[V], bool)
	load(K) (*call[V], bool)
	compareAndDelete(K, *call[V]) bool
	delete(K) (*call[V], bool)
}

type mutexBackend[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*call[V]
}

func newMutexBackend[K comparable, V any]() backend[K, V] {
	return &mutexBackend[K, V]{calls: make(map[K]*call[V])}
}

func (b *mutexBackend[K, V]) loadOrStore(key K, value *call[V]) (*call[V], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if actual, ok := b.calls[key]; ok {
		return actual, true
	}
	b.calls[key] = value
	return value, false
}

func (b *mutexBackend[K, V]) load(key K) (*call[V], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.calls[key]
	return value, ok
}

func (b *mutexBackend[K, V]) compareAndDelete(key K, expected *call[V]) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if actual, ok := b.calls[key]; !ok || actual != expected {
		return false
	}
	delete(b.calls, key)
	return true
}

func (b *mutexBackend[K, V]) delete(key K) (*call[V], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.calls[key]
	if ok {
		delete(b.calls, key)
	}
	return value, ok
}

type xsyncBackend[K comparable, V any] struct {
	calls *xsync.Map[K, *call[V]]
}

func newXsyncBackend[K comparable, V any]() backend[K, V] {
	return &xsyncBackend[K, V]{calls: xsync.NewMap[K, *call[V]]()}
}

func (b *xsyncBackend[K, V]) loadOrStore(key K, value *call[V]) (*call[V], bool) {
	return b.calls.LoadOrStore(key, value)
}

func (b *xsyncBackend[K, V]) load(key K) (*call[V], bool) { return b.calls.Load(key) }

func (b *xsyncBackend[K, V]) compareAndDelete(key K, expected *call[V]) bool {
	deleted := false
	b.calls.Compute(key, func(actual *call[V], loaded bool) (*call[V], xsync.ComputeOp) {
		if loaded && actual == expected {
			deleted = true
			return nil, xsync.DeleteOp
		}
		return actual, xsync.CancelOp
	})
	return deleted
}

func (b *xsyncBackend[K, V]) delete(key K) (*call[V], bool) {
	return b.calls.LoadAndDelete(key)
}

type shardedBackend[K comparable, V any] struct {
	seed   maphash.Seed
	shards []*mutexBackend[K, V]
}

func newShardedBackend[K comparable, V any](count int) backend[K, V] {
	shards := make([]*mutexBackend[K, V], count)
	for i := range shards {
		shards[i] = &mutexBackend[K, V]{calls: make(map[K]*call[V])}
	}
	return &shardedBackend[K, V]{seed: maphash.MakeSeed(), shards: shards}
}

func (b *shardedBackend[K, V]) shard(key K) *mutexBackend[K, V] {
	return b.shards[maphash.Comparable(b.seed, key)%uint64(len(b.shards))]
}

func (b *shardedBackend[K, V]) loadOrStore(key K, value *call[V]) (*call[V], bool) {
	return b.shard(key).loadOrStore(key, value)
}

func (b *shardedBackend[K, V]) load(key K) (*call[V], bool) {
	return b.shard(key).load(key)
}

func (b *shardedBackend[K, V]) compareAndDelete(key K, expected *call[V]) bool {
	return b.shard(key).compareAndDelete(key, expected)
}

func (b *shardedBackend[K, V]) delete(key K) (*call[V], bool) {
	return b.shard(key).delete(key)
}
