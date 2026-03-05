package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadMessages(filePath string) ([]Entry, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}

		entry, parseErr := ParseEntry(line)
		if parseErr != nil {
			continue
		}

		if entry.IsMeta || entry.Type == "progress" || entry.Type == "file-history-snapshot" {
			continue
		}
		if entry.Role == "user" || entry.Role == "assistant" {
			entries = append(entries, entry)
		}
	}
	return entries, sc.Err()
}

// LoadMessagesSummary loads only the first headN and last tailN messages from a
// session file, returning them along with the total message count.
func LoadMessagesSummary(filePath string, headN, tailN int) (head []Entry, tail []Entry, total int, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	ringIdx := 0
	rawRing := make([]string, tailN)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		if bytes.Contains(line, bIsMeta) || bytes.Contains(line, bIsMetaSpaced) {
			continue
		}
		if bytes.Contains(line, bTypeProgress) || bytes.Contains(line, bTypeProgressS) ||
			bytes.Contains(line, bTypeFileHist) || bytes.Contains(line, bTypeFileHistS) {
			continue
		}
		hasRole := bytes.Contains(line, bRoleUser) || bytes.Contains(line, bRoleUserS) ||
			bytes.Contains(line, bRoleAsst) || bytes.Contains(line, bRoleAsstS)
		if !hasRole {
			continue
		}

		total++

		if total <= headN {
			entry, parseErr := ParseEntry(string(line))
			if parseErr != nil {
				total--
				continue
			}
			head = append(head, entry)
		}

		rawRing[ringIdx%tailN] = string(line)
		ringIdx++
	}

	if err := sc.Err(); err != nil {
		return nil, nil, 0, err
	}

	if total <= headN {
		return head, nil, total, nil
	}
	tailStart := max(total-tailN, headN)
	tailCount := total - tailStart
	tail = make([]Entry, 0, tailCount)
	for i := total - tailCount; i < total; i++ {
		raw := rawRing[i%tailN]
		if entry, parseErr := ParseEntry(raw); parseErr == nil {
			tail = append(tail, entry)
		}
	}
	return head, tail, total, nil
}

func loadFileTodos(sessionID, home string) []TodoItem {
	path := filepath.Join(home, ".claude", "todos", sessionID+"-agent-"+sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= 2 {
		return nil
	}
	var todos []TodoItem
	if json.Unmarshal(data, &todos) != nil {
		return nil
	}
	return todos
}

func loadFileTasks(sessionID, home string) []TaskItem {
	dir := filepath.Join(home, ".claude", "tasks", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var tasks []TaskItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == ".lock" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var t TaskItem
		if json.Unmarshal(data, &t) == nil && t.Subject != "" {
			tasks = append(tasks, t)
		}
	}
	return tasks
}
