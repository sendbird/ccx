package tui

import "github.com/charmbracelet/bubbles/viewport"

// NavResult describes what happened after a preview navigation key press.
type NavResult int

const (
	NavUnhandled    NavResult = iota
	NavCursorMoved            // cursor moved within bounds
	NavFoldChanged            // fold/format toggled (content changed)
	NavBoundaryUp             // cursor at top, wants to go up
	NavBoundaryDown           // cursor at bottom, wants to go down
	NavSwitchToList           // left on already-folded block
	NavScrolled               // viewport scrolled (pgup/pgdown/home/end)
)

// HandleFoldNav processes navigation keys for a FoldState+viewport pair.
// Includes viewport-cursor snap: if the cursor is off-screen (e.g. after
// pgup/pgdown), up/down will first snap to a visible block before moving.
// Does NOT call any callbacks — mutates FoldState in place and returns what happened.
func HandleFoldNav(fs *FoldState, vp *viewport.Model, key string) NavResult {
	if fs == nil || fs.Collapsed == nil {
		return NavUnhandled
	}
	if len(fs.Entry.Content) == 0 {
		return NavUnhandled
	}

	// For up/down: snap cursor to viewport if off-screen
	if (key == "up" || key == "down") && snapFoldCursor(fs, vp, key) {
		return NavCursorMoved
	}

	result := fs.HandleKey(key)
	switch result {
	case foldCursorMoved:
		return NavCursorMoved
	case foldHandled:
		return NavFoldChanged
	case foldSwitchToList:
		return NavSwitchToList
	case foldBoundaryUp:
		return NavBoundaryUp
	case foldBoundaryDown:
		return NavBoundaryDown
	}

	// pgup/pgdown/home/end: scroll viewport
	if scrollViewport(vp, key) {
		return NavScrolled
	}

	return NavUnhandled
}

// HandleFlatCursorNav processes up/down for a flat integer cursor.
// Returns NavBoundaryUp/Down at edges, NavCursorMoved on success.
func HandleFlatCursorNav(cursor *int, count int, key string) NavResult {
	switch key {
	case "up", "k":
		if *cursor > 0 {
			*cursor--
			return NavCursorMoved
		}
		return NavBoundaryUp
	case "down", "j":
		if *cursor < count-1 {
			*cursor++
			return NavCursorMoved
		}
		return NavBoundaryDown
	}
	return NavUnhandled
}

// snapFoldCursor checks if the block cursor is off-screen and snaps it to
// the nearest visible block in the viewport. Returns true if snapped.
func snapFoldCursor(fs *FoldState, vp *viewport.Model, dir string) bool {
	if len(fs.BlockStarts) == 0 {
		return false
	}
	if fs.BlockCursor < 0 || fs.BlockCursor >= len(fs.BlockStarts) {
		return false
	}

	blockLine := fs.BlockStarts[fs.BlockCursor]
	vpTop := vp.YOffset
	vpBottom := vpTop + vp.Height

	// Cursor is visible — no snap needed
	if blockLine >= vpTop && blockLine < vpBottom {
		return false
	}

	// Cursor is off-screen: find nearest visible block in viewport
	switch dir {
	case "down":
		for i := 0; i < len(fs.BlockStarts); i++ {
			if !fs.isBlockVisible(i) {
				continue
			}
			if fs.BlockStarts[i] >= vpTop && fs.BlockStarts[i] < vpBottom {
				fs.BlockCursor = i
				return true
			}
		}
	case "up":
		for i := len(fs.BlockStarts) - 1; i >= 0; i-- {
			if !fs.isBlockVisible(i) {
				continue
			}
			if fs.BlockStarts[i] >= vpTop && fs.BlockStarts[i] < vpBottom {
				fs.BlockCursor = i
				return true
			}
		}
	}
	return false
}

// scrollViewport handles pgup/pgdown/home/end for a viewport.
func scrollViewport(vp *viewport.Model, key string) bool {
	switch key {
	case "pgdown":
		vp.ViewDown()
		return true
	case "pgup":
		vp.ViewUp()
		return true
	case "home":
		vp.GotoTop()
		return true
	case "end":
		vp.GotoBottom()
		return true
	}
	return false
}
