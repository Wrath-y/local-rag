package handler

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Wrath-y/local-rag/internal/store"
)

// ErrRebuildInProgress signals the documented temporary write rejection policy.
var ErrRebuildInProgress = errors.New("index rebuild in progress; writes are temporarily unavailable")

// StoreLifecycle coordinates normal requests with destructive store replacement.
type StoreLifecycle struct {
	mu         sync.RWMutex
	store      *store.Store
	rebuilding bool
}

func NewStoreLifecycle(st *store.Store) *StoreLifecycle {
	return &StoreLifecycle{store: st}
}

func (l *StoreLifecycle) WithStore(fn func(*store.Store) error) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.store == nil {
		return fmt.Errorf("store is unavailable")
	}
	return fn(l.store)
}

// WithWriteStore runs a mutation unless a rebuild has frozen writes. Taking the
// same lock used by BeginRebuild closes the admission race with snapshotting.
func (l *StoreLifecycle) WithWriteStore(fn func(*store.Store) error) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.store == nil {
		return fmt.Errorf("store is unavailable")
	}
	if l.rebuilding {
		return ErrRebuildInProgress
	}
	return fn(l.store)
}

// BeginRebuild admits one rebuild and freezes handler-level writes. Existing
// writes finish before the immutable chunk snapshot is taken.
func (l *StoreLifecycle) BeginRebuild() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.store == nil {
		return fmt.Errorf("store is unavailable")
	}
	if l.rebuilding {
		return ErrRebuildInProgress
	}
	l.rebuilding = true
	return nil
}

func (l *StoreLifecycle) EndRebuild() {
	l.mu.Lock()
	l.rebuilding = false
	l.mu.Unlock()
}

func (l *StoreLifecycle) Rebuilding() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.rebuilding
}

// Store returns the current backing store for services that do not replace the
// database file. Restore paths must continue using RestoreService so a swapped
// store is coordinated with the lifecycle lock.
func (l *StoreLifecycle) Store() *store.Store {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.store
}

// WithExclusiveStore is reserved for the short atomic cutover/rollback phase.
func (l *StoreLifecycle) WithExclusiveStore(fn func(*store.Store) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.store == nil {
		return fmt.Errorf("store is unavailable")
	}
	return fn(l.store)
}

func (l *StoreLifecycle) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.store == nil {
		return nil
	}
	err := l.store.Close()
	l.store = nil
	return err
}
