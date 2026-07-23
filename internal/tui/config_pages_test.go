package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func TestCurrentCfgPage(t *testing.T) {
	app := setupCfgApp(t)
	app.cfgPluginsPage = false
	app.cfgFilterCat = int(session.ConfigSkill)
	if got := app.currentCfgPage(); got != 2 { // SKILLS is index 2 in cfgPages
		t.Fatalf("SKILLS page index = %d, want 2", got)
	}
	app.cfgFilterCat = cfgFilterAll
	if got := app.currentCfgPage(); got != 0 {
		t.Fatalf("ALL page index = %d, want 0", got)
	}
	app.cfgPluginsPage = true
	if got := app.currentCfgPage(); got != len(cfgPages)-1 {
		t.Fatalf("PLUGINS page index = %d, want %d", got, len(cfgPages)-1)
	}
}

func TestRenderCfgPageTabsHighlightsActive(t *testing.T) {
	app := setupCfgApp(t)
	app.cfgFilterCat = int(session.ConfigSkill)
	out := stripANSI(app.renderCfgPageTabs())
	if !strings.Contains(out, "[SKILLS]") {
		t.Fatalf("SKILLS tab should be highlighted:\n%s", out)
	}
	for _, want := range []string{"ALL", "MEMORY", "SKILLS", "AGENTS", "COMMANDS", "HOOKS", "MCP", "ENTERPRISE", "PLUGINS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tabs missing %q:\n%s", want, out)
		}
	}
	app.cfgPluginsPage = true
	out = stripANSI(app.renderCfgPageTabs())
	if !strings.Contains(out, "[PLUGINS]") {
		t.Fatalf("PLUGINS tab should be highlighted:\n%s", out)
	}
}

func TestCycleCfgPageWrapsThroughPlugins(t *testing.T) {
	app := setupCfgApp(t)
	app.cfgTree = &session.ConfigTree{}
	app.plgTree = &session.PluginTree{}
	// Start at ALL (index 0). Cycle forward through every page and confirm we
	// land on PLUGINS last and wrap back to ALL.
	app.cfgPluginsPage = false
	app.cfgFilterCat = cfgFilterAll
	for i := 0; i < len(cfgPages)-1; i++ {
		app.cycleCfgPage(1)
	}
	if !app.cfgPluginsPage {
		t.Fatalf("after %d forward cycles expected PLUGINS page; cfgPluginsPage=%v", len(cfgPages)-1, app.cfgPluginsPage)
	}
	// One more wraps back to ALL.
	app.cycleCfgPage(1)
	if app.cfgPluginsPage || app.cfgFilterCat != cfgFilterAll {
		t.Fatalf("wrap to ALL failed; cfgPluginsPage=%v cfgFilterCat=%d", app.cfgPluginsPage, app.cfgFilterCat)
	}
	// Backward from ALL wraps to PLUGINS.
	app.cycleCfgPage(-1)
	if !app.cfgPluginsPage {
		t.Fatalf("backward from ALL should wrap to PLUGINS; cfgPluginsPage=%v", app.cfgPluginsPage)
	}
}

func TestEnterCfgPagePluginsSetsState(t *testing.T) {
	app := setupCfgApp(t)
	app.cfgTree = &session.ConfigTree{}
	app.plgTree = &session.PluginTree{}
	app.enterCfgPage(len(cfgPages) - 1) // PLUGINS
	if !app.cfgPluginsPage {
		t.Fatal("enterCfgPage(PLUGINS) should set cfgPluginsPage")
	}
	app.enterCfgPage(2) // SKILLS
	if app.cfgPluginsPage {
		t.Fatal("enterCfgPage(SKILLS) should clear cfgPluginsPage")
	}
	if app.cfgFilterCat != int(session.ConfigSkill) {
		t.Fatalf("cfgFilterCat = %d, want SKILLS", app.cfgFilterCat)
	}
}

func TestOpenPluginExplorerEntersConfigPluginsPage(t *testing.T) {
	app := setupCfgApp(t)
	// openPluginExplorer scans ~/.claude; point ClaudeDir at a temp dir so the
	// scan is harmless. It should land in viewConfig with cfgPluginsPage set.
	app.config.ClaudeDir = t.TempDir()
	m, _ := app.openPluginExplorer()
	if m == nil {
		t.Fatal("openPluginExplorer returned nil model")
	}
	if app.state != viewConfig {
		t.Fatalf("state = %v, want viewConfig", app.state)
	}
	if !app.cfgPluginsPage {
		t.Fatal("openPluginExplorer should set cfgPluginsPage")
	}
	_ = tea.KeyMsg{}
}

func TestEscFromPluginsWithNilCfgTreeOpensConfig(t *testing.T) {
	app := setupCfgApp(t)
	app.config.ClaudeDir = t.TempDir()
	app.openPluginExplorer()
	if !app.cfgPluginsPage {
		t.Fatal("expected PLUGINS page")
	}
	// cfgTree is nil here (entered via plugins directly). Esc must not panic.
	// The first Esc closes the open preview (HandleSplitKey); the second Esc
	// exits the PLUGINS page → openConfigExplorer (cfgPluginsPage=false).
	app.cfgSearchTerm = ""
	app.plgSearchTerm = ""
	app.plgSelectedSet = nil
	// Esc unwinds preview then page; press until we leave the PLUGINS page.
	// The key assertion is that this never panics even with cfgTree nil.
	for i := 0; i < 3 && app.cfgPluginsPage; i++ {
		app.handleConfigKeys(tea.KeyMsg{Type: tea.KeyEscape})
	}
	if app.cfgPluginsPage {
		t.Fatal("Esc should eventually leave the PLUGINS page")
	}
	if app.cfgTree == nil {
		t.Fatal("Esc should have opened the config explorer (cfgTree set)")
	}
}
