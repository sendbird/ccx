package tui

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
	"gopkg.in/yaml.v3"
)

// Preferences holds persisted view preferences that survive restarts.
type Preferences struct {
	GroupMode       string   `yaml:"group_mode,omitempty"`        // flat|proj|tree|chain|fork
	PreviewMode     string   `yaml:"preview_mode,omitempty"`      // conv|stats|mem|tasks|live
	ViewMode        string   `yaml:"view_mode,omitempty"`         // sessions|config|plugins|stats
	ConvDetailLevel int      `yaml:"conv_detail_level,omitempty"` // 0=text,1=tool,2=hook
	SplitRatio      int      `yaml:"split_ratio,omitempty"`       // 15-85
	WorktreeDir     string   `yaml:"worktree_dir,omitempty"`      // worktree subdirectory name
	HiddenBadges    []string `yaml:"hidden_badges,omitempty"`     // badge keys to hide: M,W,T,K,P,A,C,S,X,F
	FilterTerm      string   `yaml:"filter_term,omitempty"`       // last applied session filter
	EditorInput     bool     `yaml:"editor_input,omitempty"`      // true = open $EDITOR for live input
}

// KeymapsConfig groups all keybinding sections under one key.
type KeymapsConfig struct {
	Session    SessionKeymap    `yaml:"session,omitempty"`
	Actions    ActionsKeymap    `yaml:"actions,omitempty"`
	Views      ViewsKeymap      `yaml:"views,omitempty"`
	Navigation NavigationKeymap `yaml:"navigation,omitempty"`
}

// CCXConfig is the unified config file containing keybindings + preferences.
// Stored at ~/.config/ccx/config.yaml.
type CCXConfig struct {
	Keymaps     KeymapsConfig `yaml:"keymaps,omitempty"`
	Preferences Preferences   `yaml:"preferences,omitempty"`
	Shortcuts   Shortcuts     `yaml:"shortcuts,omitempty"`
}

// configPath returns the path to the unified config file.
func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ccx", "config.yaml")
}

// LoadCCXConfig reads the unified config file.
// Returns keymap, preferences, and shortcuts separately.
func LoadCCXConfig(path string) (*Keymap, Preferences, Shortcuts) {
	km := DefaultKeymap()
	var prefs Preferences
	sc := DefaultShortcuts()

	data, err := os.ReadFile(path)
	if err != nil {
		return &km, prefs, sc
	}

	var cfg CCXConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return &km, prefs, sc
	}

	// Merge keymap overrides from keymaps section
	override := Keymap{
		Session:    cfg.Keymaps.Session,
		Actions:    cfg.Keymaps.Actions,
		Views:      cfg.Keymaps.Views,
		Navigation: cfg.Keymaps.Navigation,
	}
	mergeKeymap(&km, override)

	// Merge shortcut overrides over defaults
	mergeShortcuts(sc, cfg.Shortcuts)

	return &km, cfg.Preferences, sc
}

// SavePreferences updates the preferences section in the config file,
// preserving existing keymap settings and filling in missing defaults.
func SavePreferences(prefs Preferences) {
	path := configPath()
	os.MkdirAll(filepath.Dir(path), 0755)

	// Read existing config to preserve keymap settings
	var cfg CCXConfig
	if data, err := os.ReadFile(path); err == nil {
		yaml.Unmarshal(data, &cfg)
	}

	// Fill in missing keymap defaults so the file shows all keys
	defaults := DefaultKeymap()
	fillKeymapDefaults(&cfg, defaults)

	cfg.Preferences = prefs

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return
	}

	header := "# ccx configuration\n# Keybindings: session, actions, views, navigation\n# Preferences: preferences section (auto-saved on quit)\n\n"
	os.WriteFile(path, []byte(header+string(data)), 0644)
}

// fillKeymapDefaults fills empty keymap fields with default values.
func fillKeymapDefaults(cfg *CCXConfig, d Keymap) {
	s := &cfg.Keymaps.Session
	if s.Quit == "" { s.Quit = d.Session.Quit }
	if s.Escape == "" { s.Escape = d.Session.Escape }
	if s.Open == "" { s.Open = d.Session.Open }
	if s.Edit == "" { s.Edit = d.Session.Edit }
	if s.Actions == "" { s.Actions = d.Session.Actions }
	if s.Views == "" { s.Views = d.Session.Views }
	if s.Refresh == "" { s.Refresh = d.Session.Refresh }
	if s.Group == "" { s.Group = d.Session.Group }
	if s.Help == "" { s.Help = d.Session.Help }
	if s.Search == "" { s.Search = d.Session.Search }
	if s.GlobalSearch == "" { s.GlobalSearch = d.Session.GlobalSearch }
	if s.Live == "" { s.Live = d.Session.Live }
	if s.Select == "" { s.Select = d.Session.Select }
	if s.Preview == "" { s.Preview = d.Session.Preview }
	if s.PreviewBack == "" { s.PreviewBack = d.Session.PreviewBack }
	if s.Left == "" { s.Left = d.Session.Left }
	if s.Right == "" { s.Right = d.Session.Right }
	if s.ResizeShrink == "" { s.ResizeShrink = d.Session.ResizeShrink }
	if s.ResizeGrow == "" { s.ResizeGrow = d.Session.ResizeGrow }
	if s.Command == "" { s.Command = d.Session.Command }

	a := &cfg.Keymaps.Actions
	if a.Delete == "" { a.Delete = d.Actions.Delete }
	if a.Move == "" { a.Move = d.Actions.Move }
	if a.Resume == "" { a.Resume = d.Actions.Resume }
	if a.CopyPath == "" { a.CopyPath = d.Actions.CopyPath }
	if a.Worktree == "" { a.Worktree = d.Actions.Worktree }
	if a.Kill == "" { a.Kill = d.Actions.Kill }
	if a.Input == "" { a.Input = d.Actions.Input }
	if a.Jump == "" { a.Jump = d.Actions.Jump }
	if a.URLs == "" { a.URLs = d.Actions.URLs }
	if a.Files == "" { a.Files = d.Actions.Files }
	if a.ImportMem == "" { a.ImportMem = d.Actions.ImportMem }
	if a.RemoveMem == "" { a.RemoveMem = d.Actions.RemoveMem }
	if a.Fork == "" { a.Fork = d.Actions.Fork }
	if a.New == "" { a.New = d.Actions.New }
	if a.Remote == "" { a.Remote = d.Actions.Remote }

	v := &cfg.Keymaps.Views
	if v.Stats == "" { v.Stats = d.Views.Stats }
	if v.Config == "" { v.Config = d.Views.Config }
	if v.Plugins == "" { v.Plugins = d.Views.Plugins }
}

// groupModeString converts a group mode int to its string name.
func groupModeString(mode int) string {
	switch mode {
	case groupFlat:
		return "flat"
	case groupProject:
		return "proj"
	case groupTree:
		return "tree"
	case groupChain:
		return "chain"
	case groupFork:
		return "fork"
	case groupBaseProject:
		return "repo"
	}
	return ""
}

// sessPreviewString converts a session preview mode to its string name.
func sessPreviewString(mode sessPreview) string {
	switch mode {
	case sessPreviewConversation:
		return "conv"
	case sessPreviewStats:
		return "stats"
	case sessPreviewMemory:
		return "mem"
	case sessPreviewTasksPlan:
		return "tasks"
	case sessPreviewLive:
		return "live"
	}
	return ""
}

// viewStateString converts a view state int to its string name.
func viewStateString(state viewState) string {
	switch state {
	case viewSessions:
		return "sessions"
	case viewGlobalStats:
		return "stats"
	case viewConfig:
		return "config"
	case viewPlugins:
		return "plugins"
	}
	return ""
}

// quit saves preferences and returns tea.Quit.
func (a *App) quit() (tea.Model, tea.Cmd) {
	SavePreferences(a.capturePreferences())
	return a, tea.Quit
}

// capturePreferences snapshots the current app state for persistence.
func (a *App) capturePreferences() Preferences {
	var hidden []string
	for k := range a.hiddenBadges {
		if a.hiddenBadges[k] {
			hidden = append(hidden, k)
		}
	}
	// Capture active filter
	var filterTerm string
	if a.sessionList.FilterState() == list.FilterApplied {
		filterTerm = a.sessionList.FilterInput.Value()
	}

	return Preferences{
		GroupMode:       groupModeString(a.sessGroupMode),
		PreviewMode:     sessPreviewString(a.sessPreviewMode),
		ViewMode:        viewStateString(a.state),
		ConvDetailLevel: int(a.conv.previewMode),
		SplitRatio:      a.splitRatio,
		WorktreeDir:     a.config.WorktreeDir,
		HiddenBadges:    hidden,
		FilterTerm:      filterTerm,
		EditorInput:     a.editorInput,
	}
}

// applyPreferences restores persisted state. CLI flags take precedence.
func (a *App) applyPreferences(p Preferences) {
	if a.config.GroupMode == "" && p.GroupMode != "" {
		a.config.GroupMode = p.GroupMode
	}
	if a.config.PreviewMode == "" && p.PreviewMode != "" {
		a.config.PreviewMode = p.PreviewMode
	}
	if a.config.ViewMode == "" && p.ViewMode != "" {
		a.config.ViewMode = p.ViewMode
	}
	if p.ConvDetailLevel >= 0 && p.ConvDetailLevel <= 2 {
		a.conv.previewMode = p.ConvDetailLevel
	}
	if p.SplitRatio >= 15 && p.SplitRatio <= 85 {
		a.splitRatio = p.SplitRatio
	}
	if p.WorktreeDir != "" && a.config.WorktreeDir == ".worktree" {
		a.config.WorktreeDir = p.WorktreeDir
	}
	if len(p.HiddenBadges) > 0 {
		if a.hiddenBadges == nil {
			a.hiddenBadges = make(map[string]bool)
		}
		for _, b := range p.HiddenBadges {
			a.hiddenBadges[b] = true
		}
	}
	if p.FilterTerm != "" {
		a.config.SearchQuery = p.FilterTerm
	}
	a.editorInput = p.EditorInput
}
