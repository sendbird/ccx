package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// SessionKeymap defines configurable keybindings for the session list view.
type SessionKeymap struct {
	Quit         string `yaml:"quit"`
	Escape       string `yaml:"escape"`
	Open         string `yaml:"open"`
	Edit         string `yaml:"edit"`
	Actions      string `yaml:"actions"`
	Views        string `yaml:"views"`
	Refresh      string `yaml:"refresh"`
	Group        string `yaml:"group"`
	Help         string `yaml:"help"`
	Search       string `yaml:"search"`
	Live         string `yaml:"live"`
	Select       string `yaml:"select"`
	Preview      string `yaml:"preview"`
	PreviewBack  string `yaml:"preview_back"`
	Left         string `yaml:"left"`
	Right        string `yaml:"right"`
	ResizeShrink string `yaml:"resize_shrink"`
	ResizeGrow   string `yaml:"resize_grow"`
	Command      string `yaml:"command"`
}

// ActionsKeymap defines configurable keybindings for the actions menu.
type ActionsKeymap struct {
	Delete   string `yaml:"delete"`
	Move     string `yaml:"move"`
	Resume   string `yaml:"resume"`
	CopyPath string `yaml:"copy_path"`
	Worktree string `yaml:"worktree"`
	Kill     string `yaml:"kill"`
	Input    string `yaml:"input"`
	Jump     string `yaml:"jump"`
}

// ViewsKeymap defines configurable keybindings for the views menu.
type ViewsKeymap struct {
	Stats  string `yaml:"stats"`
	Config string `yaml:"config"`
}

// NavigationKeymap defines extra keybindings that alias standard navigation keys.
// The standard arrow/pgup/pgdown/home/end keys always work; these are additive.
type NavigationKeymap struct {
	Up       []string `yaml:"up"`
	Down     []string `yaml:"down"`
	Left     []string `yaml:"left"`
	Right    []string `yaml:"right"`
	PageUp   []string `yaml:"page_up"`
	PageDown []string `yaml:"page_down"`
	Home     []string `yaml:"home"`
	End      []string `yaml:"end"`
}

// Keymap holds all configurable keybindings.
type Keymap struct {
	Session    SessionKeymap    `yaml:"session"`
	Actions    ActionsKeymap    `yaml:"actions"`
	Views      ViewsKeymap      `yaml:"views"`
	Navigation NavigationKeymap `yaml:"navigation"`
}

// DefaultKeymap returns a Keymap with all hardcoded defaults.
func DefaultKeymap() Keymap {
	return Keymap{
		Session: SessionKeymap{
			Quit:         "q",
			Escape:       "esc",
			Open:         "enter",
			Edit:         "e",
			Actions:      "x",
			Views:        "v",
			Refresh:      "R",
			Group:        "",
			Help:         "?",
			Search:       "/",
			Live:         "L",
			Select:       " ",
			Preview:      "tab",
			PreviewBack:  "shift+tab",
			Left:         "left",
			Right:        "right",
			ResizeShrink: "[",
			ResizeGrow:   "]",
			Command:      ":",
		},
		Actions: ActionsKeymap{
			Delete:   "d",
			Move:     "m",
			Resume:   "r",
			CopyPath: "y",
			Worktree: "w",
			Kill:     "k",
			Input:    "i",
			Jump:     "j",
		},
		Views: ViewsKeymap{
			Stats:  "s",
			Config: "c",
		},
		Navigation: NavigationKeymap{
			Up:       []string{"k"},
			Down:     []string{"j"},
			Left:     []string{"h"},
			Right:    []string{"l"},
			PageUp:   []string{"ctrl+b"},
			PageDown: []string{"ctrl+f"},
			Home:     []string{"g"},
			End:      []string{"G"},
		},
	}
}

// LoadKeymap reads a YAML config file and merges it over defaults.
// If the file doesn't exist, defaults are returned with no error.
func LoadKeymap(path string) (*Keymap, error) {
	km := DefaultKeymap()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &km, nil
		}
		return &km, err
	}

	var override Keymap
	if err := yaml.Unmarshal(data, &override); err != nil {
		return &km, err
	}

	mergeKeymap(&km, override)
	return &km, nil
}

func mergeKeymap(dst *Keymap, src Keymap) {
	// Session
	if src.Session.Quit != "" {
		dst.Session.Quit = src.Session.Quit
	}
	if src.Session.Escape != "" {
		dst.Session.Escape = src.Session.Escape
	}
	if src.Session.Open != "" {
		dst.Session.Open = src.Session.Open
	}
	if src.Session.Edit != "" {
		dst.Session.Edit = src.Session.Edit
	}
	if src.Session.Actions != "" {
		dst.Session.Actions = src.Session.Actions
	}
	if src.Session.Views != "" {
		dst.Session.Views = src.Session.Views
	}
	if src.Session.Refresh != "" {
		dst.Session.Refresh = src.Session.Refresh
	}
	if src.Session.Group != "" {
		dst.Session.Group = src.Session.Group
	}
	if src.Session.Help != "" {
		dst.Session.Help = src.Session.Help
	}
	if src.Session.Search != "" {
		dst.Session.Search = src.Session.Search
	}
	if src.Session.Live != "" {
		dst.Session.Live = src.Session.Live
	}
	if src.Session.Select != "" {
		dst.Session.Select = src.Session.Select
	}
	if src.Session.Preview != "" {
		dst.Session.Preview = src.Session.Preview
	}
	if src.Session.PreviewBack != "" {
		dst.Session.PreviewBack = src.Session.PreviewBack
	}
	if src.Session.Left != "" {
		dst.Session.Left = src.Session.Left
	}
	if src.Session.Right != "" {
		dst.Session.Right = src.Session.Right
	}
	if src.Session.ResizeShrink != "" {
		dst.Session.ResizeShrink = src.Session.ResizeShrink
	}
	if src.Session.ResizeGrow != "" {
		dst.Session.ResizeGrow = src.Session.ResizeGrow
	}
	if src.Session.Command != "" {
		dst.Session.Command = src.Session.Command
	}

	// Actions
	if src.Actions.Delete != "" {
		dst.Actions.Delete = src.Actions.Delete
	}
	if src.Actions.Move != "" {
		dst.Actions.Move = src.Actions.Move
	}
	if src.Actions.Resume != "" {
		dst.Actions.Resume = src.Actions.Resume
	}
	if src.Actions.CopyPath != "" {
		dst.Actions.CopyPath = src.Actions.CopyPath
	}
	if src.Actions.Worktree != "" {
		dst.Actions.Worktree = src.Actions.Worktree
	}
	if src.Actions.Kill != "" {
		dst.Actions.Kill = src.Actions.Kill
	}
	if src.Actions.Input != "" {
		dst.Actions.Input = src.Actions.Input
	}
	if src.Actions.Jump != "" {
		dst.Actions.Jump = src.Actions.Jump
	}

	// Views
	if src.Views.Stats != "" {
		dst.Views.Stats = src.Views.Stats
	}
	if src.Views.Config != "" {
		dst.Views.Config = src.Views.Config
	}

	// Navigation (append, don't replace)
	if len(src.Navigation.Up) > 0 {
		dst.Navigation.Up = src.Navigation.Up
	}
	if len(src.Navigation.Down) > 0 {
		dst.Navigation.Down = src.Navigation.Down
	}
	if len(src.Navigation.Left) > 0 {
		dst.Navigation.Left = src.Navigation.Left
	}
	if len(src.Navigation.Right) > 0 {
		dst.Navigation.Right = src.Navigation.Right
	}
	if len(src.Navigation.PageUp) > 0 {
		dst.Navigation.PageUp = src.Navigation.PageUp
	}
	if len(src.Navigation.PageDown) > 0 {
		dst.Navigation.PageDown = src.Navigation.PageDown
	}
	if len(src.Navigation.Home) > 0 {
		dst.Navigation.Home = src.Navigation.Home
	}
	if len(src.Navigation.End) > 0 {
		dst.Navigation.End = src.Navigation.End
	}
}

// navKeyTypes maps canonical nav key names to tea.KeyType for synthetic KeyMsg creation.
var navKeyTypes = map[string]tea.KeyType{
	"up":     tea.KeyUp,
	"down":   tea.KeyDown,
	"left":   tea.KeyLeft,
	"right":  tea.KeyRight,
	"pgup":   tea.KeyPgUp,
	"pgdown": tea.KeyPgDown,
	"home":   tea.KeyHome,
	"end":    tea.KeyEnd,
}

// TranslateNav checks if key is a navigation alias and returns the canonical
// key name and a synthetic tea.KeyMsg. If not an alias, returns ("", original msg).
func (km *Keymap) TranslateNav(key string, msg tea.KeyMsg) (string, tea.KeyMsg) {
	type binding struct {
		keys []string
		nav  string
	}
	nav := km.Navigation
	bindings := []binding{
		{nav.Up, "up"},
		{nav.Down, "down"},
		{nav.Left, "left"},
		{nav.Right, "right"},
		{nav.PageUp, "pgup"},
		{nav.PageDown, "pgdown"},
		{nav.Home, "home"},
		{nav.End, "end"},
	}
	for _, b := range bindings {
		for _, k := range b.keys {
			if k == key {
				return b.nav, tea.KeyMsg{Type: navKeyTypes[b.nav]}
			}
		}
	}
	return "", msg
}

// displayKey converts internal key names to human-readable display strings.
func displayKey(key string) string {
	switch key {
	case " ":
		return "sp"
	case "enter":
		return "↵"
	case "esc":
		return "esc"
	case "tab":
		return "tab"
	case "shift+tab":
		return "S-tab"
	case "left":
		return "←"
	case "right":
		return "→"
	case ":":
		return ":"
	case "ctrl+g":
		return "^G"
	default:
		return key
	}
}

// isNavKey returns true if the key message is a navigation key (arrows, pgup/pgdn, home/end).
// Used to prevent bubbles list from entering filter mode on character keys.
func isNavKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight,
		tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
		return true
	}
	return false
}

// fmtKey returns "displayKey:desc" for use in formatHelp().
func fmtKey(key, desc string) string {
	return displayKey(key) + ":" + desc
}
