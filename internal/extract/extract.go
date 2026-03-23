package extract

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/keyolk/ccx/internal/session"
)

// Item represents a URL or file path extracted from a session.
type Item struct {
	URL      string
	Label    string // short display label
	Category string // github, jira, slack, pr, other
}

// urlRegex matches http/https URLs in text.
var urlRegex = regexp.MustCompile(`https?://[^\s<>"'\x60\x29\x5D]+`)

// Package-level vars to avoid per-call allocation.
var (
	urlCleanReplacer = strings.NewReplacer(`\n`, "", `\t`, "", `\r`, "")
	jsonEscReplacer  = strings.NewReplacer(`\/`, `/`, `\\`, `\`)
	categoryOrder    = map[string]int{"pr": 0, "github": 1, "jira": 2, "slack": 3, "other": 4}
	cachedHome       string
	cachedHomeOnce   sync.Once
)

// SessionURLs loads all messages from a session file and extracts unique URLs.
func SessionURLs(filePath string) []Item {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil
	}
	return EntryURLs(entries)
}

// EntryURLs extracts unique URLs from a set of entries.
func EntryURLs(entries []session.Entry) []Item {
	seen := make(map[string]bool)
	var items []Item
	for _, entry := range entries {
		extractURLsFromBlocks(entry.Content, seen, &items)
	}
	sortItems(items)
	return items
}

// BlockURLs extracts unique URLs from content blocks.
func BlockURLs(blocks []session.ContentBlock) []Item {
	seen := make(map[string]bool)
	var items []Item
	extractURLsFromBlocks(blocks, seen, &items)
	sortItems(items)
	return items
}

// extractURLsFromBlocks appends unique URLs from blocks to items.
func extractURLsFromBlocks(blocks []session.ContentBlock, seen map[string]bool, items *[]Item) {
	for _, block := range blocks {
		for _, text := range [2]string{block.Text, block.ToolInput} {
			if text == "" {
				continue
			}
			for _, raw := range urlRegex.FindAllString(text, -1) {
				u := CleanURL(raw)
				if u == "" || seen[u] {
					continue
				}
				seen[u] = true
				*items = append(*items, CategorizeURL(u))
			}
		}
	}
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		return categoryOrder[items[i].Category] < categoryOrder[items[j].Category]
	})
}

// CleanURL strips JSON escape artifacts and trailing punctuation.
func CleanURL(raw string) string {
	// Strip literal \n, \t, \r that leak from JSON string values
	raw = urlCleanReplacer.Replace(raw)
	// Strip trailing backslashes (escaped newlines in JSON)
	raw = strings.TrimRight(raw, `\`)
	// Strip trailing punctuation that leaks from prose/markdown
	raw = strings.TrimRight(raw, ".,;:!?)'\"")

	// Validate
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	// Reject URLs with control chars or obviously broken hosts
	if strings.ContainsAny(u.Host, " \t\n\\") {
		return ""
	}
	return raw
}

// CategorizeURL classifies a URL and generates a short label.
func CategorizeURL(u string) Item {
	parsed, _ := url.Parse(u)
	host := strings.ToLower(parsed.Host)

	switch {
	case strings.Contains(host, "github.com"):
		label := githubLabel(parsed)
		cat := "github"
		if strings.Contains(parsed.Path, "/pull/") {
			cat = "pr"
		}
		return Item{URL: u, Label: label, Category: cat}

	case strings.Contains(host, "atlassian.net"):
		label := jiraLabel(parsed)
		return Item{URL: u, Label: label, Category: "jira"}

	case strings.Contains(host, "slack.com"):
		return Item{URL: u, Label: slackLabel(parsed), Category: "slack"}

	default:
		label := u
		if len(label) > 80 {
			label = label[:77] + "..."
		}
		return Item{URL: u, Label: label, Category: "other"}
	}
}

func githubLabel(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && (parts[2] == "pull" || parts[2] == "issues") {
		return fmt.Sprintf("%s/%s#%s", parts[0], parts[1], parts[3])
	}
	if len(parts) >= 2 {
		return strings.Join(parts[:2], "/")
	}
	return u.Path
}

func jiraLabel(u *url.URL) string {
	path := u.Path
	if strings.Contains(path, "/browse/") {
		idx := strings.Index(path, "/browse/")
		return path[idx+len("/browse/"):]
	}
	if len(path) > 50 {
		return path[:47] + "..."
	}
	return path
}

func slackLabel(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "archives" {
		return "slack#" + parts[1]
	}
	return "slack"
}

// OpenInBrowser opens a URL in the default browser.
func OpenInBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// ShortenPath creates a display label from a file path.
func ShortenPath(path string) string {
	cachedHomeOnce.Do(func() { cachedHome, _ = os.UserHomeDir() })
	return session.ShortenPath(path, cachedHome)
}

// JSONField extracts a string field value from a JSON string.
// Handles both "field":"value" and "field": "value" (with optional space).
func JSONField(jsonStr, field string) string {
	needle := `"` + field + `":`
	idx := strings.Index(jsonStr, needle)
	if idx < 0 {
		return ""
	}
	rest := jsonStr[idx+len(needle):]
	// Skip optional whitespace between : and opening quote
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:] // skip opening quote
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return jsonEscReplacer.Replace(rest[:end])
}
