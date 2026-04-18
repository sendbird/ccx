package session

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// cachedSession is the on-disk representation of a cached session.
type cachedSession struct {
	ModTime time.Time
	Sess    Session
}

// sessionCache maps file path → cached session metadata.
type sessionCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]cachedSession
	dirty   bool
}

func cacheFilePath(claudeDir string) string {
	return filepath.Join(claudeDir, ".ccx-cache.gob")
}

func loadCache(claudeDir string) *sessionCache {
	sc := &sessionCache{
		path:    cacheFilePath(claudeDir),
		entries: make(map[string]cachedSession),
	}

	f, err := os.Open(sc.path)
	if err != nil {
		return sc
	}
	defer f.Close()

	var entries map[string]cachedSession
	if err := gob.NewDecoder(f).Decode(&entries); err != nil {
		return sc
	}
	sc.entries = entries
	return sc
}

func (sc *sessionCache) lookup(path string, modTime time.Time) (Session, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	cached, ok := sc.entries[path]
	if !ok || !cached.ModTime.Equal(modTime) {
		return Session{}, false
	}
	return cached.Sess, true
}

func (sc *sessionCache) store(path string, modTime time.Time, sess Session) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.entries[path] = cachedSession{ModTime: modTime, Sess: sess}
	sc.dirty = true
}

func (sc *sessionCache) save() {
	if !sc.dirty {
		return
	}
	f, err := os.Create(sc.path)
	if err != nil {
		return
	}
	defer f.Close()
	gob.NewEncoder(f).Encode(sc.entries)
}

// LoadCachedSessions loads all sessions from the cache file without any
// filesystem scanning. Returns nil if no cache exists. Used for instant
// first paint at startup.
func LoadCachedSessions(claudeDir string) []Session {
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		claudeDir = filepath.Join(home, ".claude")
	}

	sc := loadCache(claudeDir)
	if len(sc.entries) == 0 {
		return nil
	}

	sessions := make([]Session, 0, len(sc.entries))
	for _, cached := range sc.entries {
		if cached.Sess.MsgCount > 0 {
			refreshSessionDerivedState(&cached.Sess, filepath.Dir(claudeDir))
			sessions = append(sessions, cached.Sess)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.After(sessions[j].ModTime)
	})

	return sessions
}

// prune removes entries for files that no longer exist.
func (sc *sessionCache) prune(validPaths map[string]bool) {
	for p := range sc.entries {
		if !validPaths[p] {
			delete(sc.entries, p)
			sc.dirty = true
		}
	}
}
