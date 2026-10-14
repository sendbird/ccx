package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/prefs"
)

// prefsApp names ccx's state file: $XDG_STATE_HOME/ccx/ui.json.
const prefsApp = "ccx"

// persistUIState turns ui.json on. The package's TestMain turns it off: the
// saver writes while the state changes, and one test's leftover grouping in
// a shared state dir would become the next test's startup state. Tests of
// persistence itself turn it back on over a private XDG_STATE_HOME.
var persistUIState = true

// uiState is the part of Preferences that ccx writes on its own — where the
// user left the list, preview and conversation pane — as opposed to settings
// a person edits (worktree_dir, initial_focus), which stay in config.yaml.
//
// It lives in ui.json through tuikit/prefs: written off the UI loop with a
// temp file and rename, debounced while it changes, flushed once on quit. It
// used to be re-marshalled into config.yaml synchronously on quit, so a crash
// lost it and every quit rewrote the user's config file.
type uiState struct {
	GroupMode       string   `json:"group_mode,omitempty"`
	PrevGroupMode   string   `json:"prev_group_mode,omitempty"`
	PreviewMode     string   `json:"preview_mode,omitempty"`
	ViewMode        string   `json:"view_mode,omitempty"`
	ConvDetailLevel int      `json:"conv_detail_level,omitempty"`
	SplitRatio      int      `json:"split_ratio,omitempty"`
	HiddenBadges    []string `json:"hidden_badges,omitempty"`
	FilterTerm      string   `json:"filter_term,omitempty"`
	EditorInput     bool     `json:"editor_input,omitempty"`
	FoldedGroups    []string `json:"folded_groups,omitempty"`
}

// uiStateOf extracts the state fields. Slices are sorted: they come from map
// iteration, and an unstable order would look like a change on every frame.
func uiStateOf(p Preferences) uiState {
	s := uiState{
		GroupMode:       p.GroupMode,
		PrevGroupMode:   p.PrevGroupMode,
		PreviewMode:     p.PreviewMode,
		ViewMode:        p.ViewMode,
		ConvDetailLevel: p.ConvDetailLevel,
		SplitRatio:      p.SplitRatio,
		HiddenBadges:    slices.Sorted(slices.Values(p.HiddenBadges)),
		FilterTerm:      p.FilterTerm,
		EditorInput:     p.EditorInput,
		FoldedGroups:    slices.Sorted(slices.Values(p.FoldedGroups)),
	}
	return s
}

// applyTo writes the state fields over p, leaving the settings alone.
func (s uiState) applyTo(p *Preferences) {
	p.GroupMode = s.GroupMode
	p.PrevGroupMode = s.PrevGroupMode
	p.PreviewMode = s.PreviewMode
	p.ViewMode = s.ViewMode
	p.ConvDetailLevel = s.ConvDetailLevel
	p.SplitRatio = s.SplitRatio
	p.HiddenBadges = s.HiddenBadges
	p.FilterTerm = s.FilterTerm
	p.EditorInput = s.EditorInput
	p.FoldedGroups = s.FoldedGroups
}

func (s uiState) equal(o uiState) bool {
	return s.GroupMode == o.GroupMode && s.PrevGroupMode == o.PrevGroupMode &&
		s.PreviewMode == o.PreviewMode && s.ViewMode == o.ViewMode &&
		s.ConvDetailLevel == o.ConvDetailLevel && s.SplitRatio == o.SplitRatio &&
		slices.Equal(s.HiddenBadges, o.HiddenBadges) && s.FilterTerm == o.FilterTerm &&
		s.EditorInput == o.EditorInput && slices.Equal(s.FoldedGroups, o.FoldedGroups)
}

// loadUIState overlays ui.json on the preferences read from config.yaml. The
// config values are the defaults, so the first run after the move keeps what
// the old build had saved there. A corrupt file is reported, not fatal.
func loadUIState(p Preferences) (Preferences, error) {
	if !persistUIState {
		return p, nil
	}
	s, err := prefs.Load(prefsApp, uiStateOf(p))
	s.applyTo(&p)
	return p, err
}

// saveUIState schedules a debounced write when the state differs from the
// last one handed to the saver. Pick mode never persists (its saver is zero).
func (a *App) saveUIState() tea.Cmd {
	if a.config.PickMode || a.quitting {
		return nil
	}
	cur := uiStateOf(a.capturePreferences())
	if cur.equal(a.lastUI) {
		return nil
	}
	a.lastUI = cur
	var cmd tea.Cmd
	a.uiSaver, cmd = a.uiSaver.Save(cur)
	return cmd
}

// handlePrefsMsg routes the saver's tick and write results. ok is false for
// any other message.
func (a *App) handlePrefsMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case prefs.SaveTick:
		var cmd tea.Cmd
		a.uiSaver, cmd = a.uiSaver.Update(msg)
		return cmd, true
	case prefs.Saved:
		if msg.Err != nil {
			a.flash("Saving UI state failed: " + msg.Err.Error())
		}
		if msg.Final {
			return tea.Quit, true
		}
		return nil, true
	}
	return nil, false
}
