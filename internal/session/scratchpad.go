package session

import (
	"io"
	"io/fs"
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
	Name      string // path relative to the scratchpad root ("pr/issue.md")
	Path      string // absolute path
	Size      int64
	ModTime   int64 // unix seconds; avoids importing time in callers
	IsText    bool
	Truncated bool // true when Body is a prefix of a larger text file
	Body      string
	// IsRepo marks a checked-out git working tree that was listed as one row
	// instead of walked. Path points at the directory.
	IsRepo bool
}

// scratchpadMaxBody caps how much of a scratchpad file we read into memory for
// preview. Files larger than this are read only up to the cap and flagged via
// Truncated so the caller can render a "(truncated)" marker.
const scratchpadMaxBody = 256 * 1024

// scratchpadMaxFiles caps how many files one scratchpad contributes. Sessions
// that unpack an archive or generate per-cell manifests hit five figures; the
// listing is a digest of what the session produced, not a file manager. When
// the cap bites, the most recently modified files win — those are the ones the
// session was actually working on.
const scratchpadMaxFiles = 300

// scratchpadMaxTotalBody caps the summed body bytes across all files. Without
// it a recursive walk over a few hundred 256KB files would pull ~75MB into
// memory on every preview.
const scratchpadMaxTotalBody = 4 * 1024 * 1024

// scratchpadMaxVisits bounds the walk itself, which the file cap cannot: the
// cost is in traversing directory entries, and that is paid before any file is
// selected. One measured scratchpad holds 70k entries across 18k directories
// and takes ~1.2s to walk fully — long enough to freeze the preview, since this
// runs on the UI thread. At 10k entries the walk costs ~136ms. Hitting the
// budget sets Truncated on the synthetic listing rather than failing quietly.
const scratchpadMaxVisits = 10000

// scratchpadSkipDirs are never descended into: build/cache output and
// dependency trees are not session products, and listing them buries the files
// that are.
var scratchpadSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true,
	"__pycache__": true, ".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true,
	".venv": true, "venv": true, ".tox": true, ".terraform": true,
}

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

// LoadScratchpadFiles walks the given session's scratchpad directory
// recursively and returns every file, sorted by path. Returns nil when the
// directory is absent or empty. projectPath is the session's ProjectPath (the
// unencoded absolute path); sessionID is the session UUID.
//
// The walk is recursive because agents organize their scratchpad into
// subdirectories (pr/issue.md, kp/pr/pr.md). A top-level-only listing silently
// dropped those — the session's own summary would point at "scratchpad/kp/pr/
// pr.md" while the preview showed nothing.
//
// Recursion means the walk can also wander into a repository the session
// cloned into its scratchpad, so it is bounded three ways: .git and dependency
// /build directories are never descended into, the file count is capped at
// scratchpadMaxFiles, and summed body bytes at scratchpadMaxTotalBody. Once a
// cap is hit, remaining files are still listed (name/size/mtime) with empty
// bodies rather than dropped, so the listing stays honest about what exists.
func LoadScratchpadFiles(projectPath, sessionID string) []ScratchpadFile {
	if projectPath == "" || sessionID == "" {
		return nil
	}
	root := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if _, err := os.Stat(root); err != nil {
		return nil
	}

	var files []ScratchpadFile
	visits := 0
	truncatedWalk := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		visits++
		if visits > scratchpadMaxVisits {
			truncatedWalk = true
			return fs.SkipAll
		}
		if err != nil {
			// An unreadable subdirectory must not abort the whole walk — the
			// rest of the scratchpad is still worth listing.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			// .git carries thousands of objects and is never a session product.
			if name == ".git" || scratchpadSkipDirs[strings.ToLower(name)] {
				return fs.SkipDir
			}
			// A repository the session cloned into its scratchpad is upstream
			// code, not session output. Walking it buried the files the session
			// actually wrote: one karpenter checkout filled all 300 slots and
			// pushed the session's own pr/issue.md out of the listing entirely.
			// It is listed as a single row so the clone is still visible.
			if isGitWorkTree(path) {
				info, ierr := d.Info()
				if ierr == nil {
					rel, rerr := filepath.Rel(root, path)
					if rerr != nil {
						rel = name
					}
					files = append(files, ScratchpadFile{
						Name:    filepath.ToSlash(rel) + "/",
						Path:    path,
						ModTime: info.ModTime().Unix(),
						IsRepo:  true,
						Body:    "(git repository)",
					})
				}
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, sockets, devices
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = d.Name()
		}
		files = append(files, ScratchpadFile{
			Name:    filepath.ToSlash(rel),
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		})
		return nil
	})
	if err != nil && len(files) == 0 {
		return nil
	}
	if len(files) == 0 {
		return nil
	}

	// Trim by recency, not by walk order: a walk is alphabetical, so capping
	// mid-walk would keep whatever sorts first rather than whatever the session
	// last touched.
	trimmed := truncatedWalk
	if len(files) > scratchpadMaxFiles {
		trimmed = true
		sort.SliceStable(files, func(i, j int) bool {
			return files[i].ModTime > files[j].ModTime
		})
		files = files[:scratchpadMaxFiles]
	}

	// Bodies are read only after trimming, so the budget is spent on files that
	// survived rather than on whatever the walk happened to reach first.
	var bodyBytes int64
	for i := range files {
		if files[i].IsRepo {
			continue
		}
		if bodyBytes >= scratchpadMaxTotalBody {
			// Past the budget the file still belongs in the listing; only its
			// content is withheld.
			files[i].IsText = true
			files[i].Truncated = true
			continue
		}
		data, truncated, rerr := readCapped(files[i].Path, files[i].Size)
		if rerr != nil {
			files[i].Body = "(unreadable)"
			continue
		}
		files[i].IsText = isLikelyText(data)
		files[i].Truncated = truncated
		if files[i].IsText {
			files[i].Body = string(data)
		} else {
			files[i].Body = "(binary file)"
		}
		bodyBytes += int64(len(data))
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})
	if trimmed {
		// A silently short listing reads as "this is everything". Say so
		// instead, so the absence of a file is not mistaken for proof it does
		// not exist.
		files = append(files, ScratchpadFile{
			Name:   ScratchpadTruncatedMarker,
			Path:   root,
			IsText: true,
			Body:   "(listing truncated — open the directory to see the rest)",
		})
	}
	return files
}

// ScratchpadTruncatedMarker names the synthetic row appended when the listing
// could not cover the whole directory. Renderers show it as a note, not a file.
const ScratchpadTruncatedMarker = "… (truncated)"

// isGitWorkTree reports whether dir is the root of a git checkout. Both a
// normal clone (.git directory) and a worktree/submodule (.git file) count.
func isGitWorkTree(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
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
