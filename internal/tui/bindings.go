package tui

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/keys"
)

// One binding table drives dispatch, footers, action hint boxes and the "?"
// overlay. Keys live in Keymap (defaults + ~/.config/ccx keymap yaml); this
// table adds what each binding is called in a footer (label) and in the help
// overlay (help). Footers and help look entries up by scope + yaml name, so a
// user override changes every surface at once and a binding without a table
// entry fails TestBindingTableCoversKeymap instead of silently showing a stale
// hand-written key.

// bindingSpec describes one Keymap field.
type bindingSpec struct {
	scope string // "session", "actions", "views", "conversation", "preview"
	name  string // yaml tag of the Keymap field
	label string // short footer / hint-box word
	help  string // "?" overlay description
}

var bindingTable = []bindingSpec{
	{"session", "quit", "quit", "Quit"},
	{"session", "escape", "back", "Back / close"},
	{"session", "open", "open", "Open / expand"},
	{"session", "edit", "edit", "Edit session files"},
	{"session", "actions", "actions", "Actions menu"},
	{"session", "views", "views", "Switch views (stats/config/plugins)"},
	{"session", "refresh", "refresh", "Refresh"},
	{"session", "group", "group", "Cycle grouping"},
	{"session", "help", "help", "This help"},
	{"session", "search", "search", "Search / filter"},
	{"session", "global_search", "global", "Global search"},
	{"session", "live", "live", "Live preview"},
	{"session", "switch", "switch", "Jump to the session's tmux window"},
	{"session", "select", "select", "Multi-select"},
	{"session", "preview", "mode", "Preview / cycle mode"},
	{"session", "preview_back", "prev-mode", "Previous preview mode"},
	{"session", "left", "close", "Unfocus / close preview"},
	{"session", "right", "focus", "Open / focus preview"},
	{"session", "resize_shrink", "shrink", "Shrink list"},
	{"session", "resize_grow", "grow", "Grow list"},
	{"session", "command", "cmd", "Command palette (full actions)"},
	{"session", "pick", "pick", "Pick (pick mode)"},
	{"session", "fold_all", "fold", "Fold all groups"},
	{"session", "expand_all", "expand", "Expand all groups"},
	{"session", "fold_group", "fold-group", "Fold group at cursor"},
	{"session", "state_menu", "states", "Toggle session states shown (live/done/…)"},
	{"session", "daily_view", "daily", "Daily view (dates + what each day produced)"},
	{"session", "page_menu", "page", "Page menu"},

	{"actions", "delete", "delete", "Delete session"},
	{"actions", "move", "move", "Move to another project"},
	{"actions", "resume", "resume", "Resume"},
	{"actions", "copy_path", "copy-path", "Copy path"},
	{"actions", "worktree", "worktree", "Move into a git worktree"},
	{"actions", "kill", "kill", "Stop the live Claude"},
	{"actions", "input", "input", "Send input to the live pane"},
	{"actions", "jump", "jump", "Jump to the live tmux pane"},
	{"actions", "urls", "urls", "URLs"},
	{"actions", "open_link", "open-link", "Open the item's link in the browser"},
	{"actions", "files", "files", "Files"},
	{"actions", "changes", "changes", "Changes"},
	{"actions", "copy", "copy", "Copy"},
	{"actions", "tags", "tags", "Tags"},
	{"actions", "import_mem", "import-mem", "Import memory from the main checkout"},
	{"actions", "remove_mem", "rm-mem", "Remove project memory"},
	{"actions", "fork", "fork", "Fork session"},
	{"actions", "new", "new", "New session here"},
	{"actions", "remote", "remote", "Run remotely"},
	{"actions", "edit", "edit", "Edit session files"},

	{"views", "stats", "stats", "Global stats"},
	{"views", "config", "config", "Config explorer"},
	{"views", "plugins", "plugins", "Plugins"},

	{"conversation", "jump_to_tree", "jump", "Jump to origin turn"},
	{"conversation", "switch_region", "region", "Switch region"},
	{"conversation", "execution_contexts", "contexts", "Execution rail"},
	{"conversation", "region_up", "region↑", "Region up"},
	{"conversation", "region_down", "region↓", "Region down"},
	{"conversation", "live_toggle", "live", "Toggle live tail"},
	{"conversation", "edit", "edit", "Edit"},
	{"conversation", "actions", "actions", "Actions"},
	{"conversation", "input", "input", "Live input (tmux)"},

	{"preview", "fold_all", "fold", "Fold all blocks"},
	{"preview", "expand_all", "expand", "Expand all blocks"},
	{"preview", "filter", "filter", "Filter blocks"},
	{"preview", "copy_mode", "copy", "Copy mode"},
	{"preview", "copy_all", "copy-all", "Copy all"},
}

func lookupBinding(scope, name string) (bindingSpec, bool) {
	for _, b := range bindingTable {
		if b.scope == scope && b.name == name {
			return b, true
		}
	}
	return bindingSpec{}, false
}

// scopeStruct returns the Keymap section for scope, addressable so the
// returned field pointers alias km.
func (km *Keymap) scopeStruct(scope string) (reflect.Value, bool) {
	var p any
	switch scope {
	case "session":
		p = &km.Session
	case "actions":
		p = &km.Actions
	case "views":
		p = &km.Views
	case "conversation":
		p = &km.Conversation
	case "preview":
		p = &km.Preview
	default:
		return reflect.Value{}, false
	}
	return reflect.ValueOf(p).Elem(), true
}

// bindingKey returns the key currently bound to scope/name ("" when unbound
// or unknown).
func (km *Keymap) bindingKey(scope, name string) string {
	v, ok := km.scopeStruct(scope)
	if !ok {
		return ""
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("yaml") == name && t.Field(i).Type.Kind() == reflect.String {
			return v.Field(i).String()
		}
	}
	return ""
}

// kitStacks are the dispatch scopes as tuikit contexts. Each scope is its own
// switch (the actions menu, the views menu, …), so each is its own stack:
// the same key in two scopes is not a conflict.
var kitStacks = [][]keys.Ctx{{"session"}, {"actions"}, {"views"}, {"conversation"}, {"preview"}}

// bindingField returns a pointer to the Keymap field for scope/name, or nil.
func (km *Keymap) bindingField(scope, name string) *string {
	v, ok := km.scopeStruct(scope)
	if !ok {
		return nil
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("yaml") == name && t.Field(i).Type.Kind() == reflect.String {
			return v.Field(i).Addr().Interface().(*string)
		}
	}
	return nil
}

// kitKeymap is the binding table as a tuikit keys.Keymap: one binding per
// bound table entry, keyed by the current (possibly user-overridden) key plus
// any live legacy alias, so tuikit's checks see exactly what dispatch sees.
func (km *Keymap) kitKeymap() keys.Keymap {
	aliases := km.legacyAliases()
	bs := make([]keys.Binding, 0, len(bindingTable))
	for _, b := range bindingTable {
		k := km.bindingKey(b.scope, b.name)
		if k == "" {
			continue
		}
		ks := []string{k}
		field := km.bindingField(b.scope, b.name)
		for _, al := range aliases[b.scope] {
			if al.current == field && al.old != k && km.resolveLegacyKey(b.scope, al.old) == k {
				ks = append(ks, al.old)
			}
		}
		bs = append(bs, keys.Binding{
			Action: b.scope + "." + b.name,
			Keys:   ks,
			Ctx:    keys.Ctx(b.scope),
			Help:   b.help,
			Short:  b.label,
		})
	}
	return keys.New(bs...)
}

// Conflicts reports keys bound to more than one action in the same scope
// (dispatch is a switch over these keys, so the second binding would be dead)
// and actions none of whose keys a 2-set Korean layout can type. The checks
// are tuikit's keys.Keymap.Conflicts over kitKeymap.
func (km *Keymap) Conflicts() []string {
	var out []string
	for _, c := range km.kitKeymap().Conflicts(kitStacks...) {
		out = append(out, c.String())
	}
	sort.Strings(out)
	return out
}

// resolveSplitKey maps a user-configured Left/Right/ResizeShrink/ResizeGrow
// key onto the canonical key the split pane dispatches on, so an override in
// the keymap yaml actually moves the binding the footer advertises.
func (km *Keymap) resolveSplitKey(key string) string {
	for _, m := range []struct{ configured, canonical string }{
		{km.Session.Left, "left"},
		{km.Session.Right, "right"},
		{km.Session.ResizeShrink, "["},
		{km.Session.ResizeGrow, "]"},
	} {
		if m.configured != "" && m.configured != m.canonical && key == m.configured {
			return m.canonical
		}
	}
	return key
}

// footerHint is one "key:label" token of a footer.
type footerHint struct {
	key, label string
	pinned     bool // never dropped when the footer is too narrow
}

// keep marks h as pinned (e.g. the esc that leaves the current view).
func (h footerHint) keep() footerHint { h.pinned = true; return h }

// fh is a hint for a structural key that is not configurable (arrows, tab,
// digits) — those are handled by the split pane / list widgets directly.
func fh(key, label string) footerHint { return footerHint{key: key, label: label} }

// bind returns the footer hint for a table binding, using its table label.
// An unbound action yields an empty hint that renderers skip.
func (a *App) bind(scope, name string) footerHint {
	spec, _ := lookupBinding(scope, name)
	return a.bindAs(scope, name, spec.label)
}

// bindAs is bind with a context-specific label ("open" reads "jump" in some
// panes) — the key still comes from the table.
func (a *App) bindAs(scope, name, label string) footerHint {
	k := a.keymap.bindingKey(scope, name)
	if k == "" {
		return footerHint{}
	}
	return footerHint{key: displayKey(k), label: label}
}

// hintsFromActions adapts the conversation interactionAction lists.
func hintsFromActions(actions ...interactionAction) []footerHint {
	out := make([]footerHint, 0, len(actions))
	for _, act := range actions {
		if act.Enabled && act.KeyLabel != "" {
			out = append(out, footerHint{key: act.KeyLabel, label: act.Label})
		}
	}
	return out
}

func renderHint(h footerHint) string {
	if h.label == "" {
		return helpKeyStyle.Render(h.key)
	}
	return helpKeyStyle.Render(h.key) + " " + helpStyle.Render(h.label)
}

// footerStyle draws footers with ccx's key/description colors. Keys arrive
// already display-named (footerHint.key), so Names is not consulted.
var footerStyle = keys.FooterStyle{Key: helpKeyStyle, Desc: helpStyle, More: helpStyle}

// footer renders hints after prefix (already-rendered badges/prompt), then
// the help and quit hints. Hints are given in priority order: when the line
// does not fit a.width, unpinned hints are dropped from the end and an
// ellipsis marks the cut, so help/quit and pinned hints stay visible.
func (a *App) footer(prefix string, hints ...footerHint) string {
	var live []footerHint
	for _, h := range hints {
		if h.key != "" {
			live = append(live, h)
		}
	}
	live = append(live, a.bind("session", "help").keep(), a.bind("session", "quit").keep())
	return renderFooter(prefix, live, a.width)
}

// renderFooter lays hints out with tuikit's keys.Footer, which owns the
// dropping order, the ellipsis (~ under a CJK locale) and the hard width cap.
// The hints are ad-hoc (structural keys, per-view labels), so each becomes a
// one-off binding in a private context; its priority is its position, and
// its key is a unique placeholder so two hints showing the same key cannot
// shadow each other — KeyLabel carries what is displayed.
func renderFooter(prefix string, hints []footerHint, width int) string {
	bs := make([]keys.Binding, 0, len(hints))
	for i, h := range hints {
		bs = append(bs, keys.Binding{
			Action:   fmt.Sprintf("hint%d", i),
			Keys:     []string{fmt.Sprintf("\x00hint%d", i)},
			KeyLabel: h.key,
			Ctx:      "footer",
			Help:     h.label,
			Footer:   i + 1,
			Pinned:   h.pinned,
		})
	}
	avail := 1 << 20 // no width yet (first frame): never cut
	if width > 0 {
		avail = max(1, width-lipgloss.Width(prefix)-2)
	}
	return prefix + "  " + keys.New(bs...).Footer([]keys.Ctx{"footer"}, avail, keys.Opts{}, footerStyle)
}

// helpRowFor is a "?" overlay row for a table binding.
func (a *App) helpRowFor(scope, name string) helpRow {
	spec, _ := lookupBinding(scope, name)
	return helpRow{displayKey(a.keymap.bindingKey(scope, name)), spec.help}
}

// actionLine builds a hint-box line from actions-scope names, using the
// table labels.
func (a *App) actionLine(names ...string) []interactionAction {
	out := make([]interactionAction, 0, len(names))
	for _, n := range names {
		spec, _ := lookupBinding("actions", n)
		out = append(out, bindAction("", a.keymap.bindingKey("actions", n), spec.label))
	}
	return out
}

// menuFooter is the footer shown while a menu opened by h is up.
func menuFooter(h footerHint, desc string) string {
	return "  " + renderHint(h) + helpStyle.Render(" — "+desc)
}

// sessionsListHelpRows is the "?" overlay for the session list: every
// session-scope binding that is bound, in table order, so the overlay cannot
// drift from dispatch. Globals live in helpCommonRows; a few rows carry extra
// context the bare table entry lacks.
func (a *App) sessionsListHelpRows() []helpRow {
	km := a.keymap
	common := map[string]bool{"command": true, "views": true, "search": true, "global_search": true, "escape": true, "help": true, "quit": true}
	var rows []helpRow
	for _, b := range bindingTable {
		if b.scope != "session" || common[b.name] || km.bindingKey("session", b.name) == "" {
			continue
		}
		switch b.name {
		case "pick":
			if !a.config.PickMode {
				continue
			}
		case "actions":
			r := a.helpRowFor("session", "actions")
			r.desc += " (" + displayKey(km.Actions.Delete) + "/" + displayKey(km.Actions.Resume) + "/" + displayKey(km.Actions.CopyPath) + "/…)"
			rows = append(rows, r)
			if km.Actions.OpenLink != "" {
				rows = append(rows, helpRow{displayKey(km.Session.Actions) + " " + displayKey(km.Actions.OpenLink), "Open the session's PR / Jira link"})
			}
			continue
		case "resize_grow":
			continue // shown with resize_shrink
		case "resize_shrink":
			rows = append(rows, helpRow{displayKey(km.Session.ResizeShrink) + " / " + displayKey(km.Session.ResizeGrow), "Resize split"})
			continue
		case "page_menu":
			rows = append(rows, a.helpRowFor("session", "page_menu"), helpRow{displayKey(km.Session.PageMenu) + " → l", "Live preview (page menu → live)"})
			continue
		}
		rows = append(rows, a.helpRowFor("session", b.name))
	}
	return rows
}
