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
// defaults to ~/.claude.
func ScanSessions(claudeDir string) ([]Session, error) {
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		claudeDir = filepath.Join(home, ".claude")
	}

	// Derive home dir for path decoding (claudeDir is typically ~/.claude)
	home := filepath.Dir(claudeDir)

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

	const numWorkers = 12
	fileCh := make(chan fileEntry, len(files))
	resultCh := make(chan Session, len(files))

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fe := range fileCh {
				sess := scanSessionStream(fe.path, fe.modTime, home)
				if sess.MsgCount > 0 {
					resultCh <- sess
				}
			}
		}()
	}

	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	sessions := make([]Session, 0, len(files))
	for sess := range resultCh {
		sessions = append(sessions, sess)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.After(sessions[j].ModTime)
	})

	return sessions, nil
}
