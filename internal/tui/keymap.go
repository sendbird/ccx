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
	GlobalSearch string `yaml:"global_search"`
	Live         string `yaml:"live"`
	Switch       string `yaml:"switch"` // jump to the session's live tmux window
	Select       string `yaml:"select"`
	Preview      string `yaml:"preview"`
	PreviewBack  string `yaml:"preview_back"`
	Left         string `yaml:"left"`
	Right        string `yaml:"right"`
	ResizeShrink string `yaml:"resize_shrink"`
	ResizeGrow   string `yaml:"resize_grow"`
	Command      string `yaml:"command"`
	Pick         string `yaml:"pick"` // pick mode only (ccx pick session)
	FoldAll      string `yaml:"fold_all"`
	ExpandAll    string `yaml:"expand_all"`
	FoldGroup    string `yaml:"fold_group"` // toggle the group at the cursor
	StateMenu    string `yaml:"state_menu"` // which session states the list shows
	DailyView    string `yaml:"daily_view"` // flip the list to date-first grouping
	PageMenu     string `yaml:"page_menu"`  // pick what the preview pane shows
}

// ActionsKeymap defines configurable keybindings for the actions menu.
type ActionsKeymap struct {
	Delete    string `yaml:"delete"`
	Move      string `yaml:"move"`
	Resume    string `yaml:"resume"`
	CopyPath  string `yaml:"copy_path"`
	Worktree  string `yaml:"worktree"`
	Kill      string `yaml:"kill"`
	Input     string `yaml:"input"`
	Jump      string `yaml:"jump"`
	URLs      string `yaml:"urls"`
	Files     string `yaml:"files"`
	Changes   string `yaml:"changes"`
	Copy      string `yaml:"copy"`
	Tags      string `yaml:"tags"`
	ImportMem string `yaml:"import_mem"`
	RemoveMem string `yaml:"remove_mem"`
	Fork      string `yaml:"fork"`
	New       string `yaml:"new"`
	Remote    string `yaml:"remote"`
	Edit      string `yaml:"edit"`
}

// ViewsKeymap defines configurable keybindings for the views menu.
type ViewsKeymap struct {
	Stats   string `yaml:"stats"`
	Config  string `yaml:"config"`
	Plugins string `yaml:"plugins"`
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

// ConvKeymap defines configurable keybindings for the conversation view.
type ConvKeymap struct {
	JumpToTree        string `yaml:"jump_to_tree"`
	SwitchRegion      string `yaml:"switch_region"`
	ExecutionContexts string `yaml:"execution_contexts"`
	RegionUp          string `yaml:"region_up"`
	RegionDown        string `yaml:"region_down"`
	LiveToggle        string `yaml:"live_toggle"`
	Edit              string `yaml:"edit"`
	Actions           string `yaml:"actions"`
	Input             string `yaml:"input"`
}

// PreviewKeymap defines configurable keybindings for focused preview panes.
type PreviewKeymap struct {
	FoldAll   string `yaml:"fold_all"`
	ExpandAll string `yaml:"expand_all"`
	Filter    string `yaml:"filter"`
	CopyMode  string `yaml:"copy_mode"`
	CopyAll   string `yaml:"copy_all"`
}

// legacyBinding records a key that used to trigger an action before the
// CJK-reachability rebind, alongside the field holding its current key.
type legacyBinding struct {
	old     string
	current *string
}

// legacyAliases returns, per scope, the pre-rebind keys that should still work.
//
// The rebind (CPLAT-12413) moved ten actions off bare uppercase letters because
// a 2-set Korean layout cannot produce them. That was real, but it also took
// those keys away from everyone typing in English, where they worked fine and
// were muscle memory — region nav had been K/J since CPLAT-10969. Neither group
// should have to lose: the new key is what the help shows, and the old key keeps
// working as an alias.
//
// Returned as pointers into km so a user's own override is respected: if they
// bound something else to the old key, the alias is dropped rather than
// shadowing their choice (see resolveLegacyKey).
func (km *Keymap) legacyAliases() map[string][]legacyBinding {
	return map[string][]legacyBinding{
		"session": {
			{"V", &km.Session.Views},
			{"D", &km.Session.DailyView},
		},
		"actions": {
			{"F", &km.Actions.Fork},
			{"M", &km.Actions.ImportMem},
			{"X", &km.Actions.RemoveMem},
		},
		"conversation": {
			{"A", &km.Conversation.ExecutionContexts},
			{"K", &km.Conversation.RegionUp},
			{"J", &km.Conversation.RegionDown},
			{"L", &km.Conversation.LiveToggle},
			{"I", &km.Conversation.Input},
		},
		"preview": {
			{"F", &km.Preview.ExpandAll},
		},
	}
}

// scopeKeys returns every key currently bound in a scope, so an alias that
// would shadow a live binding can be skipped.
func (km *Keymap) scopeKeys(scope string) map[string]bool {
	out := make(map[string]bool)
	add := func(keys ...string) {
		for _, k := range keys {
			if k != "" {
				out[k] = true
			}
		}
	}
	switch scope {
	case "session":
		s := km.Session
		add(s.Quit, s.Escape, s.Open, s.Edit, s.Actions, s.Views, s.Refresh,
			s.Group, s.Help, s.Search, s.GlobalSearch, s.Live, s.Switch,
			s.Select, s.Preview, s.PreviewBack, s.Command, s.Pick,
			s.FoldAll, s.ExpandAll, s.FoldGroup, s.StateMenu, s.DailyView, s.PageMenu)
	case "actions":
		a := km.Actions
		add(a.Delete, a.Move, a.Resume, a.CopyPath, a.Worktree, a.Kill, a.Input,
			a.Jump, a.URLs, a.Files, a.Changes, a.Copy, a.Tags, a.ImportMem,
			a.RemoveMem, a.Fork, a.New, a.Remote, a.Edit)
	case "conversation":
		c := km.Conversation
		add(c.JumpToTree, c.SwitchRegion, c.ExecutionContexts, c.RegionUp,
			c.RegionDown, c.LiveToggle, c.Edit, c.Actions, c.Input)
	case "preview":
		p := km.Preview
		add(p.FoldAll, p.ExpandAll, p.Filter, p.CopyMode, p.CopyAll)
	}
	return out
}

// resolveLegacyKey rewrites a pre-rebind key to whatever now triggers the same
// action, so both spellings work. Any other key is returned unchanged.
//
// Call it on the key string before dispatch. Aliases that collide with a live
// binding in the same scope are ignored, so a user who rebound the old key to
// something else keeps their meaning.
func (km *Keymap) resolveLegacyKey(scope, key string) string {
	aliases, ok := km.legacyAliases()[scope]
	if !ok {
		return key
	}
	var live map[string]bool
	for _, a := range aliases {
		if a.old != key || *a.current == "" || *a.current == key {
			continue
		}
		if live == nil {
			live = km.scopeKeys(scope)
		}
		if live[key] {
			return key // the old key means something else now; leave it alone
		}
		return *a.current
	}
	return key
}

// Keymap holds all configurable keybindings.
type Keymap struct {
	Session      SessionKeymap    `yaml:"session"`
	Actions      ActionsKeymap    `yaml:"actions"`
	Views        ViewsKeymap      `yaml:"views"`
	Conversation ConvKeymap       `yaml:"conversation"`
	Preview      PreviewKeymap    `yaml:"preview"`
	Navigation   NavigationKeymap `yaml:"navigation"`
}

// DefaultKeymap returns a Keymap with all hardcoded defaults.
func DefaultKeymap() Keymap {
	return Keymap{
		Session: SessionKeymap{
			Quit:    "q",
			Escape:  "esc",
			Open:    "enter",
			Edit:    "",
			Actions: "x",
			// Lowercase: "V" was unreachable under a 2-set Korean input source
			// (see cjkReachableUpper). Nothing else claims "v" in the session list.
			Views:        "v",
			Refresh:      "R",
			Group:        "",
			Help:         "?",
			Search:       "/",
			GlobalSearch: "ctrl+s",
			Live:         "",
			Switch:       "",
			Select:       " ",
			Preview:      "tab",
			PreviewBack:  "shift+tab",
			Left:         "left",
			Right:        "right",
			ResizeShrink: "[",
			ResizeGrow:   "]",
			Command:      ":",
			Pick:         "P",
			// FoldAll/ExpandAll deliberately avoid a Shift-case pair: under a
			// 2-set Korean input source most letters emit the same rune with and
			// without Shift, so "f"/"F" collapsed into one key and expand-all
			// could never fire. See cjkReachableUpper.
			FoldAll:   "f",
			ExpandAll: "u",
			FoldGroup: "o",
			StateMenu: "s",
			// "d", not "D": uppercase is unreachable under a Korean input source
			// and nothing else claims "d" at the session-list top level.
			DailyView: "d",
			PageMenu:  "p",
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
			URLs:     "u",
			Files:    "f",
			Changes:  "g",
			Copy:     "c",
			Tags:     "t",
			// Lowercase because "M"/"X"/"F" were unreachable under a 2-set Korean
			// input source, and each also collided with the lowercase action
			// listed earlier in the switch (Move/Actions-menu/Files), which won.
			// a = add memory, z = zap memory, b = branch off.
			ImportMem: "a",
			RemoveMem: "z",
			Fork:      "b",
			New:       "n",
			Remote:    "R",
			Edit:      "e",
		},
		Views: ViewsKeymap{
			Stats:   "s",
			Config:  "c",
			Plugins: "p",
		},
		Conversation: ConvKeymap{
			JumpToTree:   "o",
			SwitchRegion: "P",
			// The rest were uppercase and so unreachable under a 2-set Korean
			// input source (see cjkReachableUpper). Region nav uses ctrl+p/ctrl+n
			// rather than ctrl+k/ctrl+j because ctrl+j is LF and terminals may
			// deliver it as Enter; the others take a free lowercase key.
			ExecutionContexts: "a",
			RegionUp:          "ctrl+p",
			RegionDown:        "ctrl+n",
			LiveToggle:        "ctrl+l",
			Edit:              "e",
			Actions:           "x",
			Input:             "w",
		},
		Preview: PreviewKeymap{
			FoldAll: "f",
			// "u" (not "F") for the same reason as Session.ExpandAll, and to
			// match it — block-level and group-level expand share one key.
			ExpandAll: "u",
			Filter:    "/",
			CopyMode:  "v",
			CopyAll:   "y",
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

// cjkReachableUpper returns the uppercase ASCII letters a user can actually
// produce while a CJK input source is active.
//
// Under a 2-set Korean layout, Shift only yields a distinct character on the
// keys carrying a doubled consonant or ㅒ/ㅖ (q/w/e/r/t/o/p). Every other key
// emits the same jamo with and without Shift, and this bubbletea version's
// tea.Key has no Shift field, so the distinction is genuinely lost before it
// reaches us — a shortcut bound to, say, "F" can never fire. Binding two
// actions to a case pair ("f"/"F") is therefore a latent bug: under a Korean
// IME both presses land on whichever case the switch tests first.
//
// Derived from the langmap rather than hardcoded so the two cannot drift.
func cjkReachableUpper() map[string]bool {
	out := make(map[string]bool)
	for _, latin := range defaultHangulToLatin() {
		if len(latin) == 1 && latin[0] >= 'A' && latin[0] <= 'Z' {
			out[latin] = true
		}
	}
	return out
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
	if src.Session.GlobalSearch != "" {
		dst.Session.GlobalSearch = src.Session.GlobalSearch
	}
	if src.Session.Live != "" {
		dst.Session.Live = src.Session.Live
	}
	if src.Session.Switch != "" {
		dst.Session.Switch = src.Session.Switch
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
	if src.Session.Pick != "" {
		dst.Session.Pick = src.Session.Pick
	}
	if src.Session.FoldAll != "" {
		dst.Session.FoldAll = src.Session.FoldAll
	}
	if src.Session.ExpandAll != "" {
		dst.Session.ExpandAll = src.Session.ExpandAll
	}
	if src.Session.FoldGroup != "" {
		dst.Session.FoldGroup = src.Session.FoldGroup
	}
	if src.Session.StateMenu != "" {
		dst.Session.StateMenu = src.Session.StateMenu
	}
	if src.Session.DailyView != "" {
		dst.Session.DailyView = src.Session.DailyView
	}
	if src.Session.PageMenu != "" {
		dst.Session.PageMenu = src.Session.PageMenu
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
	if src.Actions.URLs != "" {
		dst.Actions.URLs = src.Actions.URLs
	}
	if src.Actions.Files != "" {
		dst.Actions.Files = src.Actions.Files
	}
	if src.Actions.Changes != "" {
		dst.Actions.Changes = src.Actions.Changes
	}
	if src.Actions.Copy != "" {
		dst.Actions.Copy = src.Actions.Copy
	}
	if src.Actions.ImportMem != "" {
		dst.Actions.ImportMem = src.Actions.ImportMem
	}
	if src.Actions.RemoveMem != "" {
		dst.Actions.RemoveMem = src.Actions.RemoveMem
	}
	if src.Actions.Fork != "" {
		dst.Actions.Fork = src.Actions.Fork
	}
	if src.Actions.New != "" {
		dst.Actions.New = src.Actions.New
	}
	if src.Actions.Remote != "" {
		dst.Actions.Remote = src.Actions.Remote
	}
	if src.Actions.Tags != "" {
		dst.Actions.Tags = src.Actions.Tags
	}
	if src.Actions.Edit != "" {
		dst.Actions.Edit = src.Actions.Edit
	}

	// Views
	if src.Views.Stats != "" {
		dst.Views.Stats = src.Views.Stats
	}
	if src.Views.Config != "" {
		dst.Views.Config = src.Views.Config
	}
	if src.Views.Plugins != "" {
		dst.Views.Plugins = src.Views.Plugins
	}

	// Conversation
	if src.Conversation.JumpToTree != "" {
		dst.Conversation.JumpToTree = src.Conversation.JumpToTree
	}
	if src.Conversation.SwitchRegion != "" {
		dst.Conversation.SwitchRegion = src.Conversation.SwitchRegion
	}
	if src.Conversation.ExecutionContexts != "" {
		dst.Conversation.ExecutionContexts = src.Conversation.ExecutionContexts
	}
	if src.Conversation.RegionUp != "" {
		dst.Conversation.RegionUp = src.Conversation.RegionUp
	}
	if src.Conversation.RegionDown != "" {
		dst.Conversation.RegionDown = src.Conversation.RegionDown
	}
	if src.Conversation.LiveToggle != "" {
		dst.Conversation.LiveToggle = src.Conversation.LiveToggle
	}
	if src.Conversation.Edit != "" {
		dst.Conversation.Edit = src.Conversation.Edit
	}
	if src.Conversation.Actions != "" {
		dst.Conversation.Actions = src.Conversation.Actions
	}
	if src.Conversation.Input != "" {
		dst.Conversation.Input = src.Conversation.Input
	}

	// Preview
	if src.Preview.FoldAll != "" {
		dst.Preview.FoldAll = src.Preview.FoldAll
	}
	if src.Preview.ExpandAll != "" {
		dst.Preview.ExpandAll = src.Preview.ExpandAll
	}
	if src.Preview.Filter != "" {
		dst.Preview.Filter = src.Preview.Filter
	}
	if src.Preview.CopyMode != "" {
		dst.Preview.CopyMode = src.Preview.CopyMode
	}
	if src.Preview.CopyAll != "" {
		dst.Preview.CopyAll = src.Preview.CopyAll
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

// resolveConversationConflicts repairs conversation-view key bindings that
// collide with the region-navigation keys (RegionUp/RegionDown). Region nav is
// the backbone of moving between the RESOURCES / CONVERSATION / EXECUTION
// CONTEXTS panes, and its handler runs before the other conversation actions in
// handleConversationKeys — so if a stale user config maps another action to the
// same key (historically jump_to_tree defaulted to "J", the RegionDown key),
// that action silently swallows the keypress and region nav appears broken.
//
// Rather than depend on dispatch order, we detect the collision at load time and
// snap the offending action back to its default, keeping region nav intact.
func (km *Keymap) resolveConversationConflicts() {
	defaults := DefaultKeymap().Conversation
	up := km.Conversation.RegionUp
	down := km.Conversation.RegionDown
	collides := func(key string) bool {
		return key != "" && (key == up || key == down)
	}
	if collides(km.Conversation.JumpToTree) {
		km.Conversation.JumpToTree = defaults.JumpToTree
	}
	if collides(km.Conversation.SwitchRegion) {
		km.Conversation.SwitchRegion = defaults.SwitchRegion
	}
	if collides(km.Conversation.ExecutionContexts) {
		km.Conversation.ExecutionContexts = defaults.ExecutionContexts
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
