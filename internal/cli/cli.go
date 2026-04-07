package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// Run executes a CLI subcommand (urls or files) and prints results to stdout.
// Returns an error if no session can be found in the current tmux window.
func Run(command, claudeDir string) error {
	filePath, err := findSessionFile(claudeDir)
	if err != nil {
		return err
	}

	var items []extract.Item
	switch command {
	case "urls":
		items = extract.SessionURLs(filePath)
	case "files":
		items = extract.SessionFilePaths(filePath)
	default:
		return fmt.Errorf("unknown command: %s", command)
	}

	if len(items) == 0 {
		return fmt.Errorf("no %s found in session", command)
	}

	for _, item := range items {
		cat := strings.ToUpper(item.Category)
		if len(cat) < 5 {
			cat += strings.Repeat(" ", 5-len(cat))
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", cat, item.Label, item.URL)
	}
	return nil
}

// findSessionFile detects the Claude session in the same tmux window
// and returns its JSONL file path.
func findSessionFile(claudeDir string) (string, error) {
	projPaths := tmux.CurrentWindowClaudes()
	if len(projPaths) == 0 {
		// Non-tmux fallback
		live := tmux.FindLiveProjectPaths()
		for p := range live {
			projPaths = append(projPaths, p)
		}
	}
	if len(projPaths) == 0 {
		return "", fmt.Errorf("no Claude session found in current window")
	}

	sessions := session.LoadCachedSessions(claudeDir)
	if len(sessions) == 0 {
		return "", fmt.Errorf("no cached sessions found (run ccx once first)")
	}

	// Match project path → most recently modified session
	for _, projPath := range projPaths {
		absProj, _ := filepath.Abs(projPath)
		if absProj == "" {
			absProj = projPath
		}
		var best *session.Session
		for i := range sessions {
			absSP, _ := filepath.Abs(sessions[i].ProjectPath)
			if absSP == "" {
				absSP = sessions[i].ProjectPath
			}
			if absSP == absProj {
				if best == nil || sessions[i].ModTime.After(best.ModTime) {
					best = &sessions[i]
				}
			}
		}
		if best != nil {
			return best.FilePath, nil
		}
	}

	return "", fmt.Errorf("no session found matching project paths: %s", strings.Join(projPaths, ", "))
}
