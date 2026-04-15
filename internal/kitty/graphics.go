package kitty

import (
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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

// ImageSize returns the pixel dimensions of an image file.
// Returns (0, 0) if the file can't be read or decoded.
func ImageSize(path string) (width, height int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// FitSize computes cell dimensions that fit within maxCols x maxRows
// while preserving the image's aspect ratio.
// Assumes ~2:1 cell aspect ratio (cells are roughly twice as tall as wide).
func FitSize(imgW, imgH, maxCols, maxRows int) (cols, rows int) {
	if imgW <= 0 || imgH <= 0 || maxCols <= 0 || maxRows <= 0 {
		return maxCols, maxRows
	}
	// Cell aspect: each cell is ~2x taller than wide in pixels
	// So 1 row ≈ 2 cols in pixel height
	aspectW := float64(imgW)
	aspectH := float64(imgH) / 2.0 // convert pixel height to cell-equivalent width

	scaleW := float64(maxCols) / aspectW
	scaleH := float64(maxRows) / aspectH
	scale := scaleW
	if scaleH < scale {
		scale = scaleH
	}

	cols = int(aspectW * scale)
	rows = int(aspectH * scale)
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if cols > maxCols {
		cols = maxCols
	}
	if rows > maxRows {
		rows = maxRows
	}
	return cols, rows
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

var (
	cachedPaneTop  int
	cachedPaneLeft int
	paneOffsetOnce sync.Once
)

// PaneOffset returns the tmux pane's top-left position in the terminal.
// Returns (0, 0) if not inside tmux or if the query fails.
// Cached after first call; call InvalidatePaneOffset() on resize.
func PaneOffset() (top, left int) {
	if os.Getenv("TMUX") == "" {
		return 0, 0
	}
	paneOffsetOnce.Do(fetchPaneOffset)
	return cachedPaneTop, cachedPaneLeft
}

// InvalidatePaneOffset forces re-query of tmux pane position on next call.
func InvalidatePaneOffset() {
	paneOffsetOnce = sync.Once{}
}

func fetchPaneOffset() {
	out, err := exec.Command("tmux", "display-message", "-p", "#{pane_top} #{pane_left}").Output()
	if err != nil {
		return
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return
	}
	fmt.Sscanf(parts[0], "%d", &cachedPaneTop)
	fmt.Sscanf(parts[1], "%d", &cachedPaneLeft)
}

// PlaceImage returns escape sequences to move the cursor to (row, col) and
// then display an image there. Row and col are 1-based coordinates relative
// to the application pane. Inside tmux, the cursor move and image draw are
// both sent through DCS passthrough with absolute terminal coordinates.
func PlaceImage(path string, row, col, cols, rows int) string {
	if path == "" {
		return ""
	}
	img := DisplayImage(path, cols, rows)
	if img == "" {
		return ""
	}
	if inTmux() {
		// Inside tmux: cursor move + image must both go through passthrough
		// with absolute terminal coordinates
		top, left := PaneOffset()
		absRow := row + top
		absCol := col + left
		cursor := fmt.Sprintf("\x1b[%d;%dH", absRow, absCol)
		raw := cursor + fmt.Sprintf("\x1b_Ga=T,t=f,f=100,c=%d,r=%d;%s\x1b\\", cols, rows,
			base64.StdEncoding.EncodeToString([]byte(path)))
		return wrapForTmux(raw)
	}
	// Not in tmux: simple cursor move + image
	cursor := fmt.Sprintf("\x1b[%d;%dH", row, col)
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
