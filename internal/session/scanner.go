package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScanSessions scans for Claude Code sessions. If claudeDir is empty,
// defaults to ~/.claude. Uses a metadata cache to avoid re-scanning
// unchanged files.
func ScanSessions(claudeDir string) ([]Session, error) {
	if claudeDir == "" {
		claudeDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		claudeDir = filepath.Join(home, ".claude")
	}

	// Derive home dir for path decoding (claudeDir is typically ~/.claude)
	home := filepath.Dir(claudeDir)

	// Load badge store
	badgeStore := LoadBadges(claudeDir)

	projectsDir := filepath.Join(claudeDir, "projects")
	if _, statErr := os.Stat(projectsDir); os.IsNotExist(statErr) {
		return nil, nil
	}

	type fileEntry struct {
		path    string
		modTime time.Time
		size    int64
	}
	var files []fileEntry
	var err error
	err = filepath.Walk(projectsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == "subagents" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".jsonl") || strings.HasPrefix(info.Name(), "agent-") {
			return nil
		}
		files = append(files, fileEntry{path: path, modTime: info.ModTime(), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk projects dir: %w", err)
	}

	// Load cache and separate hits from misses
	cache := loadCache(claudeDir)
	validPaths := make(map[string]bool, len(files))
	var needScan []fileEntry

	sessions := make([]Session, 0, len(files))
	for _, f := range files {
		validPaths[f.path] = true
		if cached, ok := cache.lookup(f.path, f.modTime); ok {
			if cached.MsgCount > 0 {
				// Load custom badges for cached sessions
				cached.CustomBadges = badgeStore.Get(cached.ID)
				sessions = append(sessions, cached)
			}
			continue
		}
		needScan = append(needScan, f)
	}

	// Scan only files that changed or are new
	if len(needScan) > 0 {
		const numWorkers = 12
		fileCh := make(chan fileEntry, len(needScan))
		resultCh := make(chan Session, len(needScan))

		var wg sync.WaitGroup
		for range min(numWorkers, len(needScan)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for fe := range fileCh {
					sess := scanSessionStream(fe.path, fe.modTime, home, badgeStore)
					cache.store(fe.path, fe.modTime, sess)
					if sess.MsgCount > 0 {
						resultCh <- sess
					}
				}
			}()
		}

		for _, f := range needScan {
			fileCh <- f
		}
		close(fileCh)

		go func() {
			wg.Wait()
			close(resultCh)
		}()

		for sess := range resultCh {
			sessions = append(sessions, sess)
		}
	}

	// Prune deleted files and persist cache
	cache.prune(validPaths)
	cache.save()

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.After(sessions[j].ModTime)
	})

	return sessions, nil
}

// ScanSessionsForPaths scans only the most recently modified session file in
// each project directory matching the given absolute project paths. This is
// designed for fast live-session detection at startup.
func ScanSessionsForPaths(claudeDir string, projectPaths []string) ([]Session, error) {
	if len(projectPaths) == 0 {
		return nil, nil
	}

	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		claudeDir = filepath.Join(home, ".claude")
	}

	home := filepath.Dir(claudeDir)

	// Load badge store
	badgeStore := LoadBadges(claudeDir)

	projectsDir := filepath.Join(claudeDir, "projects")

	var sessions []Session
	for _, pp := range projectPaths {
		encoded := EncodeProjectPath(pp)
		dir := filepath.Join(projectsDir, encoded)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		// Find the most recently modified JSONL file
		var bestPath string
		var bestTime time.Time
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasPrefix(e.Name(), "agent-") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(bestTime) {
				bestTime = info.ModTime()
				bestPath = filepath.Join(dir, e.Name())
			}
		}
		if bestPath != "" {
			sess := scanSessionStream(bestPath, bestTime, home, badgeStore)
			if sess.MsgCount > 0 {
				sessions = append(sessions, sess)
			}
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.After(sessions[j].ModTime)
	})

	return sessions, nil
}
