package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// BadgeStore manages custom user-created badges for sessions
type BadgeStore struct {
	mu      sync.Mutex
	path    string
	mapping map[string][]string // sessionID → badge names
}

// LoadBadges loads the badge mapping from ~/.claude/badges.json
func LoadBadges(home string) *BadgeStore {
	path := filepath.Join(home, "badges.json")
	bs := &BadgeStore{
		path:    path,
		mapping: make(map[string][]string),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist yet, start with empty mapping
		return bs
	}

	_ = json.Unmarshal(data, &bs.mapping)
	return bs
}

// Get returns the badges for a given session ID
func (bs *BadgeStore) Get(sessionID string) []string {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	badges, ok := bs.mapping[sessionID]
	if !ok {
		return nil
	}
	// Return copy to avoid external mutation
	result := make([]string, len(badges))
	copy(result, badges)
	return result
}

// Set updates the badges for a session
func (bs *BadgeStore) Set(sessionID string, badges []string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if len(badges) == 0 {
		delete(bs.mapping, sessionID)
	} else {
		bs.mapping[sessionID] = badges
	}
}

// RemoveBadgeFromAll removes a badge from all sessions and returns count of affected sessions
func (bs *BadgeStore) RemoveBadgeFromAll(badgeName string) int {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	count := 0
	for sessID, badges := range bs.mapping {
		filtered := make([]string, 0, len(badges))
		for _, b := range badges {
			if b != badgeName {
				filtered = append(filtered, b)
			}
		}
		if len(filtered) != len(badges) {
			count++
			if len(filtered) == 0 {
				delete(bs.mapping, sessID)
			} else {
				bs.mapping[sessID] = filtered
			}
		}
	}
	return count
}

// AllBadges returns a sorted list of all distinct badge names
func (bs *BadgeStore) AllBadges() []string {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	seen := make(map[string]bool)
	for _, badges := range bs.mapping {
		for _, b := range badges {
			seen[b] = true
		}
	}

	result := make([]string, 0, len(seen))
	for b := range seen {
		result = append(result, b)
	}
	sort.Strings(result)
	return result
}

// CountBadgeUsage returns how many sessions use a given badge
func (bs *BadgeStore) CountBadgeUsage(badgeName string) int {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	count := 0
	for _, badges := range bs.mapping {
		for _, b := range badges {
			if b == badgeName {
				count++
				break
			}
		}
	}
	return count
}

// Save persists the badge mapping to disk
func (bs *BadgeStore) Save() error {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	data, err := json.MarshalIndent(bs.mapping, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(bs.path, data, 0644)
}
