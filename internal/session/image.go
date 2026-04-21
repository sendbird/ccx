package session

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
)

// ImageCachePath returns the cached image file path for an image block's paste ID.
// Returns empty string if the file doesn't exist.
func ImageCachePath(homeDir, sessionID string, pasteID int) string {
	if pasteID <= 0 || sessionID == "" {
		return ""
	}
	p := filepath.Join(homeDir, ".claude", "image-cache", sessionID, fmt.Sprintf("%d.png", pasteID))
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// imageCacheDir returns the cache directory for a session, creating it if
// needed. Returns empty string on error.
func imageCacheDir(homeDir, sessionID string) string {
	if homeDir == "" || sessionID == "" {
		return ""
	}
	dir := filepath.Join(homeDir, ".claude", "image-cache", sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	return dir
}

// ExtractImageToTemp extracts an image by paste ID from a session JSONL file,
// decodes the base64 data, writes it to the shared image cache as PNG (so the
// same path is stable across renders and so the Kitty f=100 PNG protocol
// accepts it), and returns the path. Falls back to a scratch temp file if
// the cache directory can't be created.
func ExtractImageToTemp(homeDir, sessionFilePath, sessionID string, pasteID int) (string, error) {
	// Try cache first
	if p := ImageCachePath(homeDir, sessionID, pasteID); p != "" {
		return p, nil
	}

	// Scan JSONL for the entry with this paste ID
	f, err := os.Open(sessionFilePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	type imgSource struct {
		Data      string `json:"data"`
		MediaType string `json:"media_type"`
	}
	type imgBlock struct {
		Type   string     `json:"type"`
		Source *imgSource `json:"source"`
	}
	type imgMsg struct {
		Content json.RawMessage `json:"content"`
	}
	type imgEntry struct {
		ImagePasteIDs []int           `json:"imagePasteIds"`
		Message       json.RawMessage `json:"message"`
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry imgEntry
		if json.Unmarshal(line, &entry) != nil {
			continue
		}

		// Check if this entry has our paste ID
		targetIdx := -1
		for i, id := range entry.ImagePasteIDs {
			if id == pasteID {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			continue
		}

		// Parse message content to find the image block
		var msg imgMsg
		if json.Unmarshal(entry.Message, &msg) != nil {
			continue
		}
		var blocks []imgBlock
		if json.Unmarshal(msg.Content, &blocks) != nil {
			continue
		}

		// Find the N-th image block
		imgIdx := 0
		for _, b := range blocks {
			if b.Type == "image" {
				if imgIdx == targetIdx && b.Source != nil && b.Source.Data != "" {
					data, err := base64.StdEncoding.DecodeString(b.Source.Data)
					if err != nil {
						return "", fmt.Errorf("decode base64: %w", err)
					}
					return writeImageCache(homeDir, sessionID, pasteID, data, b.Source.MediaType)
				}
				imgIdx++
			}
		}
	}

	return "", fmt.Errorf("image #%d not found in session", pasteID)
}

// writeImageCache persists image bytes as PNG under the per-session image
// cache so the Kitty file-path protocol has a stable, PNG-encoded source. If
// the source is already PNG we can drop the bytes straight onto disk;
// otherwise we re-encode. Falls back to os.CreateTemp when the cache dir is
// unavailable.
func writeImageCache(homeDir, sessionID string, pasteID int, data []byte, mediaType string) (string, error) {
	dir := imageCacheDir(homeDir, sessionID)
	if dir == "" {
		return writeScratchImage(pasteID, data, mediaType)
	}
	dst := filepath.Join(dir, fmt.Sprintf("%d.png", pasteID))

	if mediaType == "image/png" {
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return writeScratchImage(pasteID, data, mediaType)
		}
		return dst, nil
	}

	// Non-PNG source (JPEG, GIF, etc.) — decode and re-encode as PNG so
	// the Kitty f=100 protocol accepts it as a file path source.
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return writeScratchImage(pasteID, data, mediaType)
	}
	out, err := os.Create(dst)
	if err != nil {
		return writeScratchImage(pasteID, data, mediaType)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		os.Remove(dst)
		return writeScratchImage(pasteID, data, mediaType)
	}
	return dst, nil
}

// writeScratchImage is the fallback path when the cache directory is
// unwritable. It writes a unique temp file keyed by pasteID; repeat calls
// will create additional temp files, but at least the current render gets a
// valid file.
func writeScratchImage(pasteID int, data []byte, mediaType string) (string, error) {
	ext := ".png"
	if mediaType == "image/jpeg" {
		ext = ".jpg"
	}
	tmp, err := os.CreateTemp("", fmt.Sprintf("ccx-img-%d-*%s", pasteID, ext))
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()
	return tmp.Name(), nil
}
