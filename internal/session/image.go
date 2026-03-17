package session

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// ExtractImageToTemp extracts an image by paste ID from a session JSONL file,
// decodes the base64 data, writes it to a temp file, and returns the path.
// Falls back to the image cache if available.
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
					// Decode and write to temp file
					data, err := base64.StdEncoding.DecodeString(b.Source.Data)
					if err != nil {
						return "", fmt.Errorf("decode base64: %w", err)
					}
					ext := ".png"
					if b.Source.MediaType == "image/jpeg" {
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
				imgIdx++
			}
		}
	}

	return "", fmt.Errorf("image #%d not found in session", pasteID)
}
