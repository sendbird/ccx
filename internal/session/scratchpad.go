package session

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ScratchpadFile is one file from a session's scratchpad directory. Claude Code
// allocates a per-session scratchpad under /tmp/claude-<uid>/<enc-project>/<sid>/scratchpad/
// for ephemeral working files. Body is the file content for text files (capped
// at scratchpadMaxBody bytes, with Truncated set when the file is larger);
// binary files carry a placeholder instead.
type ScratchpadFile struct {
	Name      string // base filename
	Path      string // absolute path
	Size      int64
	ModTime   int64 // unix seconds; avoids importing time in callers
	IsText    bool
	Truncated bool // true when Body is a prefix of a larger text file
	Body      string
}

// scratchpadMaxBody caps how much of a scratchpad file we read into memory for
// preview. Files larger than this are read only up to the cap and flagged via
// Truncated so the caller can render a "(truncated)" marker.
const scratchpadMaxBody = 256 * 1024

// scratchpadBaseOverride lets tests redirect ScratchpadBase to a temp dir
// without touching /tmp. Empty in production.
var scratchpadBaseOverride string

// ScratchpadBase returns the root directory Claude Code uses for per-session
// scratchpads: /tmp/claude-<uid> (on macOS /tmp is a symlink to /private/tmp).
// Claude Code does NOT honor $TMPDIR for this — it hardcodes /tmp — so we do
// too. Callers should treat a missing directory as "no scratchpad" rather than
// an error.
func ScratchpadBase() string {
	if scratchpadBaseOverride != "" {
		return scratchpadBaseOverride
	}
	return filepath.Join("/tmp", "claude-"+itoa(os.Getuid()))
}

// SetScratchpadBaseOverride is a test seam that redirects ScratchpadBase to the
// given directory so callers in other packages can exercise scratchpad loading
// hermetically. Returns a restore func. Production code must not call it.
func SetScratchpadBaseOverride(dir string) func() {
	prev := scratchpadBaseOverride
	scratchpadBaseOverride = dir
	return func() { scratchpadBaseOverride = prev }
}

// LoadScratchpadFiles reads and parses every file in the given session's
// scratchpad directory, sorted by name. Returns nil when the directory is
// absent or empty. projectPath is the session's ProjectPath (the unencoded
// absolute path); sessionID is the session UUID.
func LoadScratchpadFiles(projectPath, sessionID string) []ScratchpadFile {
	if projectPath == "" || sessionID == "" {
		return nil
	}
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []ScratchpadFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		sf := ScratchpadFile{
			Name:    e.Name(),
			Path:    full,
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		}
		data, truncated, err := readCapped(full, info.Size())
		if err == nil {
			sf.IsText = isLikelyText(data)
			sf.Truncated = truncated
			if sf.IsText {
				sf.Body = string(data)
			} else {
				sf.Body = "(binary file)"
			}
		} else {
			sf.Body = "(unreadable)"
		}
		files = append(files, sf)
	}

	if len(files) == 0 {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})
	return files
}

// readCapped reads up to scratchpadMaxBody bytes from path, returning the
// content, whether the file exceeded the cap, and any read error. Avoids
// loading huge files fully into memory.
func readCapped(path string, size int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if size <= scratchpadMaxBody {
		data, err := io.ReadAll(f)
		return data, false, err
	}
	data, err := io.ReadAll(io.LimitReader(f, scratchpadMaxBody))
	return data, true, err
}

// isLikelyText returns true for UTF-8 decodable data with no NUL bytes — good
// enough to decide whether to render file contents or a binary placeholder.
func isLikelyText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if !utf8.Valid(data) {
		return false
	}
	return !strings.ContainsRune(string(data), 0)
}
