package tui

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/sendbird/ccx/internal/extract"
)

// overlayCase opens one overlay. prep builds the app in the view the overlay
// belongs to (nil means the sessions list); setup raises the overlay itself.
type overlayCase struct {
	name  string
	prep  func(t *testing.T) *App
	setup func(a *App)
}

func prepConversation(t *testing.T) *App {
	t.Helper()
	app, _, _ := setupInspectorFlowApp(t)
	return app
}

func prepConfig(t *testing.T) *App {
	t.Helper()
	return setupCfgApp(t)
}

func prepPlugins(t *testing.T) *App {
	t.Helper()
	app := setupCfgApp(t)
	app.cfgPluginsPage = true
	app.plgSplit.Show = true
	return app
}

func prepStats(t *testing.T) *App {
	t.Helper()
	app, _ := newMouseApp(t, 3)
	app.state = viewGlobalStats
	app.globalStatsLoading = false
	app.globalStatsVP = viewport.New(app.width, app.height-3)
	app.globalStatsVP.SetContent("stats body")
	return app
}

// baseKey summarizes the base view's state, so a test can prove an overlay
// kept every mouse event away from it.
func baseKey(app *App) string {
	key := fmt.Sprintf("state=%d focus=%t/%t/%t", app.state, app.sessSplit.Focus, app.conv.split.Focus, app.cfgSplit.Focus)
	if app.state == viewSessions {
		if s, ok := app.selectedSession(); ok {
			key += " sel=" + s.ID
		}
	}
	if app.state == viewConversation {
		key += fmt.Sprintf(" conv=%d", app.convList.Index())
	}
	return key
}

func urlItemsForTest() []extract.Item {
	var items []extract.Item
	for i := range 8 {
		items = append(items, extract.Item{URL: fmt.Sprintf("https://example.com/%d", i), Label: fmt.Sprintf("link %d", i), Category: "other"})
	}
	return items
}

func overlayCases() []overlayCase {
	return []overlayCase{
		{"live input", nil, func(a *App) { a.liveInputModal = newInputModal(); a.liveInputActive = true }},
		{"cross-session search", nil, func(a *App) { a.enterSearchMode() }},
		{"confirm", nil, func(a *App) { a.confirmMsg = "Delete this session?" }},
		{"help", nil, func(a *App) { a.showHelp = true }},
		{"command palette", nil, func(a *App) { a.startCmdMode() }},
		{"url menu", nil, func(a *App) { a.openURLMenuFromItems(urlItemsForTest(), "urls") }},
		{"full text", nil, func(a *App) { a.sessConvFullText = longText() }},
		{"tag menu", nil, func(a *App) { a.tagMenu = true }},
		{"actions menu", nil, func(a *App) { a.actionsMenu = true }},
		{"page menu", nil, func(a *App) { a.sessPageMenu = true }},
		{"state menu", nil, func(a *App) { a.stateMenu = true }},
		{"views menu", nil, func(a *App) { a.viewsMenu = true }},
		{"edit menu", nil, func(a *App) { a.openEditMenu(a.sessions[0]) }},
		{"share ref", nil, func(a *App) {
			a.shareRefMenu = true
			a.shareRefStage = shareRefStageItem
			a.shareRefItems = []shareRefItem{{Label: "a", Path: "/a", Kind: "memory"}, {Label: "b", Path: "/b", Kind: "plan"}, {Label: "c", Path: "/c", Kind: "plan"}, {Label: "d", Path: "/d", Kind: "plan"}, {Label: "e", Path: "/e", Kind: "plan"}}
		}},
		{"stats page menu", prepStats, func(a *App) { a.statsPageMenu = true }},
		{"inspector menu", prepConversation, func(a *App) { a.inspectorMenu = true }},
		{"conversation actions", prepConversation, func(a *App) { a.convActionsMenu = true }},
		{"execution context menu", prepConversation, func(a *App) { a.executionContextMenu = true }},
		{"project picker", prepConfig, func(a *App) {
			ti := textinput.New()
			ti.Focus()
			a.cfgProjectInput = ti
			a.cfgProjectEntries = []cfgProjectEntry{{path: "/a", name: "a"}, {path: "/b", name: "b"}, {path: "/c", name: "c"}, {path: "/d", name: "d"}, {path: "/e", name: "e"}}
			a.cfgProjectPicker = true
		}},
		{"config actions", prepConfig, func(a *App) { a.cfgActionsMenu = true }},
		{"plugin actions", prepPlugins, func(a *App) { a.plgActionsMenu = true }},
		{"plugin component actions", prepPlugins, func(a *App) { a.plgDetailActive = true; a.plgDetailSplit.Show = true; a.plgCompActionsMenu = true }},
	}
}

func longText() string {
	s := ""
	for i := range 80 {
		s += fmt.Sprintf("line %d of the full text\n", i)
	}
	return s
}

func openOverlay(t *testing.T, tc overlayCase) *App {
	t.Helper()
	var app *App
	if tc.prep != nil {
		app = tc.prep(t)
	} else {
		app, _ = newMouseApp(t, 6)
	}
	tc.setup(app)
	if o, ok := app.activeOverlay(); !ok || o.name != tc.name {
		t.Fatalf("setup opened %q (open=%t), want %q", o.name, ok, tc.name)
	}
	if app.overlayRect().W == 0 {
		t.Fatalf("overlay drew nothing:\n%s", stripANSI(app.View()))
	}
	return app
}

func TestOverlayCasesCoverTheTable(t *testing.T) {
	have := map[string]bool{}
	for _, tc := range overlayCases() {
		have[tc.name] = true
	}
	for _, o := range mouseOverlays {
		if !have[o.name] {
			t.Errorf("overlay %q has no mouse test", o.name)
		}
	}
}

// Outside click is the overlay's Esc: it closes, and nothing underneath runs
// (the title row would otherwise be a breadcrumb navigation).
func TestOverlayOutsideClickClosesAndIsolatesBase(t *testing.T) {
	for _, tc := range overlayCases() {
		t.Run(tc.name, func(t *testing.T) {
			app := openOverlay(t, tc)
			before := baseKey(app)
			if r := app.overlayRect(); r.Contains(0, 0) {
				t.Fatal("rect covers the title row")
			}
			app = send(app, press(0, 0))
			if o, open := app.activeOverlay(); open {
				t.Fatalf("outside click left %q open", o.name)
			}
			if got := baseKey(app); got != before {
				t.Fatalf("outside click reached the base: %s -> %s", before, got)
			}
		})
	}
}

func TestOverlayInsideClickKeepsItOpenAndBaseUntouched(t *testing.T) {
	for _, tc := range overlayCases() {
		t.Run(tc.name, func(t *testing.T) {
			app := openOverlay(t, tc)
			before := baseKey(app)
			r := app.overlayRect()
			app = send(app, press(r.X+r.W/2, r.Y+r.H/2))
			if o, open := app.activeOverlay(); !open || o.name != tc.name {
				t.Fatalf("inside click closed %q", tc.name)
			}
			if got := baseKey(app); got != before {
				t.Fatalf("inside click reached the base: %s -> %s", before, got)
			}
		})
	}
}

func TestOverlayClickOnRowBehindDoesNotSelectIt(t *testing.T) {
	for _, tc := range overlayCases() {
		if tc.prep != nil {
			continue // row hit-testing is the sessions list's
		}
		t.Run(tc.name, func(t *testing.T) {
			app := openOverlay(t, tc)
			r := app.overlayRect()
			// Row s05 is drawn near the top of the list; use whichever visible
			// row is not covered by this overlay.
			for _, id := range []string{"s05  ", "s04  ", "s03  ", "s02  ", "s01  "} {
				x, y := find(t, app, id)
				if r.Contains(x, y) {
					continue
				}
				app = send(app, press(x, y))
				if got := selectedID(t, app); got != "s00" {
					t.Fatalf("click behind overlay selected %q", got)
				}
				return
			}
			t.Skip("overlay covers every visible row")
		})
	}
}

func TestOverlayWheelNeverReachesBaseOrDismisses(t *testing.T) {
	for _, tc := range overlayCases() {
		t.Run(tc.name, func(t *testing.T) {
			app := openOverlay(t, tc)
			before := baseKey(app)
			r := app.overlayRect()
			for _, down := range []bool{true, false} {
				app = send(app, wheel(r.X+1, r.Y+1, down))
			}
			if o, open := app.activeOverlay(); !open || o.name != tc.name {
				t.Fatalf("wheel dismissed %q", tc.name)
			}
			if got := baseKey(app); got != before {
				t.Fatalf("wheel reached the base: %s -> %s", before, got)
			}
		})
	}
}

func TestScrollableOverlaysFollowTheWheelByThree(t *testing.T) {
	app := openOverlay(t, overlayCase{"url menu", nil, func(a *App) { a.openURLMenuFromItems(urlItemsForTest(), "urls") }})
	r := app.overlayRect()
	app = send(app, wheel(r.X+1, r.Y+1, true))
	if app.urlCursor != 3 {
		t.Fatalf("url menu cursor = %d after one notch, want 3", app.urlCursor)
	}
	app = send(app, wheel(r.X+1, r.Y+1, false))
	if app.urlCursor != 0 {
		t.Fatalf("url menu cursor = %d after notch back, want 0", app.urlCursor)
	}

	app = openOverlay(t, overlayCase{"full text", nil, func(a *App) { a.sessConvFullText = longText() }})
	r = app.overlayRect()
	app = send(app, wheel(r.X+1, r.Y+1, true))
	if app.sessConvFullScroll != 3 {
		t.Fatalf("full text scroll = %d after one notch, want 3", app.sessConvFullScroll)
	}
}

func TestShareRefAndProjectPickerFollowTheWheel(t *testing.T) {
	for _, tc := range overlayCases() {
		if tc.name != "share ref" && tc.name != "project picker" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			app := openOverlay(t, tc)
			r := app.overlayRect()
			app = send(app, wheel(r.X+1, r.Y+1, true))
			got := app.shareRefCursor
			if tc.name == "project picker" {
				got = app.cfgProjectCursor
			}
			if got != 3 {
				t.Fatalf("cursor = %d after one notch, want 3", got)
			}
		})
	}
}
