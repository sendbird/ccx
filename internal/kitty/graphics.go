package kitty

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var (
	supported     bool
	supportedOnce sync.Once
)

// Supported returns true if the terminal supports Kitty graphics protocol.
// Checks environment variables and tmux outer terminal hints.
// Cached for the session lifetime.
func Supported() bool {
	supportedOnce.Do(func() {
		// Explicit user override
		if os.Getenv("CCX_KITTY") == "1" {
			supported = true
			return
		}
		if os.Getenv("CCX_KITTY") == "0" {
			supported = false
			return
		}
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
		// Kitty-specific env vars that may survive tmux
		if !supported {
			if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("KITTY_PID") != "" {
				supported = true
			}
		}
		// Inside tmux: check if the outer terminal is Kitty-compatible.
		// tmux stores the original TERM in its socket environment.
		if !supported && os.Getenv("TMUX") != "" {
			supported = detectKittyViaTmux()
		}
	})
	return supported
}

// detectKittyViaTmux checks tmux's server environment for Kitty indicators.
func detectKittyViaTmux() bool {
	out, err := exec.Command("tmux", "show-environment", "-g", "TERM_PROGRAM").Output()
	if err == nil {
		val := strings.TrimSpace(string(out))
		// Format: TERM_PROGRAM=kitty or -TERM_PROGRAM (unset)
		if strings.HasPrefix(val, "TERM_PROGRAM=") {
			prog := strings.TrimPrefix(val, "TERM_PROGRAM=")
			if prog == "kitty" || prog == "WezTerm" || prog == "ghostty" {
				return true
			}
		}
	}
	// Also check KITTY_WINDOW_ID in tmux environment
	out, err = exec.Command("tmux", "show-environment", "-g", "KITTY_WINDOW_ID").Output()
	if err == nil {
		val := strings.TrimSpace(string(out))
		if strings.HasPrefix(val, "KITTY_WINDOW_ID=") {
			return true
		}
	}
	return false
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
	seq := fmt.Sprintf("\x1b_Ga=T,t=f,f=100,c=%d,r=%d;%s\x1b\\", cols, rows, encoded)
	return wrapForTmux(seq)
}

// ClearImages returns Kitty graphics protocol escape sequence to clear all
// images from the screen. Should be called when leaving image display context.
func ClearImages() string {
	return wrapForTmux("\x1b_Ga=d;\x1b\\")
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

// inTmux returns true if running inside tmux.
func inTmux() bool {
	return os.Getenv("TMUX") != ""
}

// wrapForTmux wraps Kitty escape sequences for tmux passthrough.
// tmux requires DCS passthrough: \ePtmux;\e<seq>\e\\
func wrapForTmux(seq string) string {
	if !inTmux() {
		return seq
	}
	// Double all ESC bytes inside the sequence for tmux passthrough
	inner := strings.ReplaceAll(seq, "\x1b", "\x1b\x1b")
	return "\x1bPtmux;" + inner + "\x1b\\"
}
