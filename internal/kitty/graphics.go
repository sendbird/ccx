// Package kitty is ccx's thin layer over tuikit/kitty.
//
// ccx draws an inline image by appending cursor-placed, file-transmitted
// Kitty graphics sequences to the frame (DisplayImage / PlaceImage) and
// clearing them on the next frame. tuikit/kitty supplies the parts that used
// to be hand-rolled here: terminal detection (also behind tmux), the escape
// builders with tmux passthrough wrapping, the pane query, the visibility
// watcher and the allow-passthrough promotion.
//
// Detection runs tmux, so it happens once, in Init, before a program starts.
// Until then (and in tests, which never call Init) the terminal counts as
// unsupported: nothing here runs tmux or emits a sequence. The detected Caps
// and the last known pane geometry are package state because every view that
// draws an image reads them, and threading them through each model would
// change many signatures for no behavioural gain.
package kitty

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	tk "github.com/keyolk/tuikit/kitty"
)

// imageID is the one image ccx shows at a time.
const imageID = 1

// WatchInterval is how often the visibility watcher polls tmux. tmux does not
// forward focus-out on a window switch, so polling is the only signal; 250ms
// feels responsive without thrashing the tmux control channel.
const WatchInterval = 250 * time.Millisecond

var (
	mu    sync.Mutex
	caps  tk.Caps
	tmux  tk.TmuxQuerier
	pane  = tk.Pane{Visible: true}
	inits sync.Once
)

// Init detects the terminal with the real environment, asking tmux through
// q (ccx passes its tuikit/tmux Client, which satisfies tk.TmuxQuerier).
// CCX_KITTY ("1"/"0") forces the answer, as before. Call it from main before
// the program starts; later calls are no-ops.
func Init(q tk.TmuxQuerier) {
	inits.Do(func() {
		env := func(k string) string {
			if k == tk.OverrideEnv {
				return os.Getenv("CCX_KITTY")
			}
			return os.Getenv(k)
		}
		SetCaps(tk.Detect(env, q), q)
	})
}

// SetCaps installs detected capabilities and the tmux querier used for pane
// queries. Tests use it to turn graphics on without a terminal or tmux.
func SetCaps(c tk.Caps, q tk.TmuxQuerier) {
	mu.Lock()
	defer mu.Unlock()
	caps, tmux = c, q
	pane = tk.Pane{Visible: true}
}

// Current returns the installed capabilities.
func Current() tk.Caps {
	mu.Lock()
	defer mu.Unlock()
	return caps
}

// Supported reports whether the terminal speaks the Kitty graphics protocol.
func Supported() bool { return Current().Supported }

// EnsurePassthroughCmd promotes tmux's allow-passthrough to "all" off the UI
// loop (nil when there is nothing to do). With "on", tmux drops passthrough
// from a hidden pane, so an image placed while ccx's window was visible stays
// on screen after a window switch: the clear can never get through.
func EnsurePassthroughCmd() tea.Cmd {
	mu.Lock()
	c, q := caps, tmux
	mu.Unlock()
	if q == nil {
		return nil
	}
	return tk.EnsurePassthroughCmd(c, q)
}

// SetPane records the pane geometry and visibility from a pane query, so
// PlaceImage can turn pane coordinates into terminal coordinates without
// running tmux in View.
func SetPane(p tk.Pane) {
	mu.Lock()
	defer mu.Unlock()
	pane = p
}

// Pane is the last recorded pane state (visible at 0,0 until a query lands).
func Pane() tk.Pane {
	mu.Lock()
	defer mu.Unlock()
	return pane
}

// probe queries our own pane by id. Outside tmux it reports visible at 0,0.
func probe() (tk.Pane, error) {
	mu.Lock()
	c, q := caps, tmux
	mu.Unlock()
	if q == nil || !c.InTmux {
		return tk.Pane{Visible: true}, nil
	}
	return tk.QueryPane(q, c.Pane)
}

// PaneCmd queries the pane once, off the UI loop, and reports a
// tk.PaneMsg. It replaces the old cached offset's invalidation: the caller
// records the answer with SetPane.
func PaneCmd() tea.Cmd {
	if !Supported() {
		return nil
	}
	return func() tea.Msg {
		p, err := probe()
		return tk.PaneMsg{Pane: p, Err: err}
	}
}

// WatchCmd polls the pane and returns only when the answer differs from
// last, so an idle app gets no messages. Re-arm it with the message it got.
func WatchCmd(last tk.PaneMsg) tea.Cmd {
	if !Supported() {
		return nil
	}
	return tk.WatchPane(last, probe, WatchInterval)
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

// FitSize computes cell dimensions that fit within maxCols x maxRows while
// preserving the image's aspect ratio, for the default cell (twice as tall
// as wide). An unknown image size gets the whole box.
func FitSize(imgW, imgH, maxCols, maxRows int) (cols, rows int) {
	if maxCols <= 0 || maxRows <= 0 {
		return maxCols, maxRows
	}
	return tk.Fit(imgW, imgH, maxCols, maxRows, 0, 0)
}

// DisplayImage returns the sequences that transmit the image at path (the
// terminal reads the file) and place it at the cursor in cols x rows cells.
// Returns "" when unsupported or the file does not exist.
func DisplayImage(path string, cols, rows int) string {
	c := Current()
	if !c.Supported {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return c.TransmitFile(imageID, path, tk.TransmitOpts{}) + c.Place(imageID, 0, cols, rows, tk.PlaceOpts{})
}

// ClearImages returns the sequence that removes every image from the
// screen; "" when unsupported.
func ClearImages() string {
	return Current().DeleteAll()
}

// PlaceImage returns the sequences that draw the image at path with its top
// left at (row, col), 1-based and relative to ccx's pane. Inside tmux the
// cursor move goes through the same passthrough, offset by the pane's
// position from the last pane query.
func PlaceImage(path string, row, col, cols, rows int) string {
	c := Current()
	if !c.Supported || path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	o := tk.PlaceOpts{MoveTo: true, Row: row - 1, Col: col - 1}
	if c.InTmux {
		p := Pane()
		o.PaneTop, o.PaneLeft = p.Top, p.Left
	}
	return c.TransmitFile(imageID, path, tk.TransmitOpts{}) + c.Place(imageID, 0, cols, rows, o)
}
