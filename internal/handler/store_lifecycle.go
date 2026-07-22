package handler

import (
	"fmt"
	"sync"

	"github.com/Wrath-y/local-rag/internal/store"
)

// StoreLifecycle coordinates normal requests with destructive store replacement.
type StoreLifecycle struct {
	mu    sync.RWMutex
	store *store.Store
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
