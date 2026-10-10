package state

import "sync"

// Ported from core/src/state/queue.ts: a tiny in-process lock queue that
// serializes same-key runs IN CALL ORDER and lets different keys run
// concurrently. The store routes every write through it keyed by the target
// relPath, so the whole store serializes on a per-identity basis instead of
// holding a single coarse lock — independent baselines/docs never queue
// behind each other.
//
// TypeScript's promise-tail chain becomes a per-key mutex plus a waiters
// count: the entry is dropped exactly when the last registered run has
// settled (the TS `tails.delete(key)` condition), so the map cannot grow
// without bound and cannot desynchronize from the serializer.

// StateLockQueue executes runs keyed by a target identity. Run blocks until
// fn has completed under the key's serialization and propagates its error;
// keys run concurrently with each other. (The TS `run<T>` generic becomes a
// plain error return here — Go interfaces cannot declare type parameters,
// and callers capture their results in the closure.)
type StateLockQueue interface {
	Run(key string, fn func() error) error
}

// keyLock is one serialized target: the mutex callers queue on plus the count
// of registered-but-unsettled runs guarding its map entry.
type keyLock struct {
	mu      sync.Mutex
	waiters int
}

// inProcessQueue is the StateLockQueue implementation.
type inProcessQueue struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

// CreateInProcessQueue returns the default per-target queue.
func CreateInProcessQueue() StateLockQueue {
	return &inProcessQueue{locks: make(map[string]*keyLock)}
}

// Run executes fn under key, serialized against every other registered run on
// the same key and concurrent with runs on other keys.
func (q *inProcessQueue) Run(key string, fn func() error) error {
	lock := q.acquire(key)
	lock.mu.Lock()
	defer func() {
		lock.mu.Unlock()
		q.release(key, lock)
	}()
	return fn()
}

// acquire registers a run on key, returning its lock. A new registration
// always happens under q.mu, so a run can never be inserted into a lock entry
// that is being retired.
func (q *inProcessQueue) acquire(key string) *keyLock {
	q.mu.Lock()
	defer q.mu.Unlock()
	lock := q.locks[key]
	if lock == nil {
		lock = &keyLock{}
		q.locks[key] = lock
	}
	lock.waiters++
	return lock
}

// release retires the run and drops the key entry once the last registered
// run has settled (no waiter is left behind).
func (q *inProcessQueue) release(key string, lock *keyLock) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lock.waiters--
	if lock.waiters == 0 {
		delete(q.locks, key)
	}
}
