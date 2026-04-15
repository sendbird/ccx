package kitty

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
)

var (
	supported     bool
	supportedOnce sync.Once
)

// Supported returns true if the terminal supports Kitty graphics protocol.
// Detects by checking TERM_PROGRAM and TERM environment variables.
// Cached for the session lifetime.
func Supported() bool {
	supportedOnce.Do(func() {
		term := os.Getenv("TERM")
		termProg := os.Getenv("TERM_PROGRAM")
		// Known Kitty-compatible terminals
		switch {
		case termProg == "kitty":
			supported = true
		case termProg == "WezTerm":
			supported = true
		case termProg == "ghostty":
			supported = true
		case strings.HasPrefix(term, "xterm-kitty"):
			supported = true
		}
	})
	return supported
}

// DisplayImage returns Kitty graphics protocol escape sequences to display
// an image from a file path at the given terminal cell position and size.
// Uses file path transfer mode (t=f) so the terminal reads directly from disk.
// Returns empty string if the file doesn't exist.
func DisplayImage(path string, cols, rows int) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(path))
	// a=T: transmit and display
	// t=f: file path transfer
	// f=100: auto-detect format
	// c=cols, r=rows: display size in cells
	return fmt.Sprintf("\x1b_Ga=T,t=f,f=100,c=%d,r=%d;%s\x1b\\", cols, rows, encoded)
}

// ClearImages returns Kitty graphics protocol escape sequence to clear all
// images from the screen. Should be called when leaving image display context.
func ClearImages() string {
	return "\x1b_Ga=d;\x1b\\"
}

// PlaceImage returns escape sequences to move the cursor to (row, col) and
// then display an image there. Row and col are 1-based terminal coordinates.
func PlaceImage(path string, row, col, cols, rows int) string {
	if path == "" {
		return ""
	}
	cursor := fmt.Sprintf("\x1b[%d;%dH", row, col)
	img := DisplayImage(path, cols, rows)
	if img == "" {
		return ""
	}
	return cursor + img
}
