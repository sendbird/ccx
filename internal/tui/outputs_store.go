package tui

import "github.com/sendbird/ccx/internal/session"

// outputsStoreCap bounds how many sessions' digests are kept. Each entry is a
// slice of SessionOutput (names, paths, timestamps — no message bodies), so a
// few hundred is cheap next to the transcript scan it saves.
const outputsStoreCap = 256

// previewStoreCap bounds how many sessions' preview loads are kept per mode.
// Entries here can hold parsed structures (a context tree, workflow runs), so
// this is deliberately smaller than the digest cap.
const previewStoreCap = 64

// sessionStore caches per-session work across row changes.
//
// Every preview mode keeps its loaded state for exactly one session, so moving
// the cursor dropped the previous row's result and moving back re-read its
// transcript from scratch. Walking a list up and down therefore paid a full
// disk load once per row per pass, which is the "navigation feels laggy" report
// this exists to fix.
//
// Keyed by the caller's dataKey (session ID + mtime, and the mode where one
// store is shared), so a transcript that grew misses and is loaded again.
// Eviction is insertion-order, not LRU: a pass over the list touches each key
// once, so recency carries no information a cheaper policy would miss.
type sessionStore[T any] struct {
	items map[string]T
	order []string
	cap   int
}

func newSessionStore[T any](capacity int) *sessionStore[T] {
	if capacity <= 0 {
		capacity = outputsStoreCap
	}
	return &sessionStore[T]{items: make(map[string]T, capacity), cap: capacity}
}

func (s *sessionStore[T]) Get(key string) (T, bool) {
	var zero T
	if s == nil || s.items == nil {
		return zero, false
	}
	v, ok := s.items[key]
	return v, ok
}

func (s *sessionStore[T]) Set(key string, v T) {
	if s == nil || key == "" {
		return
	}
	if s.items == nil {
		s.items = make(map[string]T, s.cap)
	}
	if _, exists := s.items[key]; exists {
		s.items[key] = v
		return
	}
	if s.cap <= 0 {
		s.cap = outputsStoreCap
	}
	if len(s.order) >= s.cap {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.items, oldest)
	}
	s.order = append(s.order, key)
	s.items[key] = v
}

// outputsStore is the digest cache. It exists as its own type only to keep the
// nil-normalizing Set below, which the generic store cannot express.
type outputsStore struct {
	sessionStore[[]session.SessionOutput]
}

func newOutputsStore(capacity int) *outputsStore {
	if capacity <= 0 {
		capacity = outputsStoreCap
	}
	return &outputsStore{sessionStore[[]session.SessionOutput]{
		items: make(map[string][]session.SessionOutput, capacity), cap: capacity,
	}}
}

// Set records a collection. A nil result is stored as an empty (non-nil) slice
// so "collected, produced nothing" stays distinguishable from "never collected"
// — otherwise a session with no outputs would be rescanned on every visit.
func (s *outputsStore) Set(key string, outs []session.SessionOutput) {
	if outs == nil {
		outs = []session.SessionOutput{}
	}
	s.sessionStore.Set(key, outs)
}
