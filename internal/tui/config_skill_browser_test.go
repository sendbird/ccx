package tui

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

// writeTestSkillDir creates a fake skill directory under a temp home's
// ~/.claude/skills/<name>/ with SKILL.md plus supporting files, returning the
// skill directory path. Cleaned up via t.Cleanup.
func writeTestSkillDir(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for rel, body := range files {
		full := filepath.Join(skillDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return skillDir
}

// setupCfgApp builds a minimal App in the config view for skill-browser tests.
func setupCfgApp(t *testing.T) *App {
	t.Helper()
	app := NewApp(nil, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	app = model.(*App)
	app.state = viewConfig
	app.cfgTree = &session.ConfigTree{}
	app.cfgSplit.Show = true
	return app
}

func TestBuildSkillFileItemsRecursive(t *testing.T) {
	dir := writeTestSkillDir(t, "my-skill", map[string]string{
		"SKILL.md":               "---\nname: my-skill\n---\nbody\n",
		"references/palette.md":  "palette\n",
		"scripts/run.sh":         "#!/bin/sh\n",
		".hidden/secret.md":      "secret\n",
	})
	items := buildSkillFileItems(dir, "my-skill", "")
	if len(items) != 4 { // header + 3 visible files (hidden skipped)
		t.Fatalf("items = %d, want header + 3: %+v", len(items), items)
	}
	if ci, ok := items[0].(cfgItem); !ok || !ci.isHeader || ci.label != "SKILL: my-skill" {
		t.Fatalf("header row wrong: %+v", items[0])
	}
	rels := []string{}
	for _, it := range items[1:] {
		ci, ok := it.(cfgItem)
		if !ok {
			t.Fatalf("non-cfgItem: %+v", it)
		}
		rels = append(rels, ci.item.Name)
		if ci.item.Path == "" {
			t.Fatalf("file row %q has empty Path", ci.item.Name)
		}
		if _, err := os.Stat(ci.item.Path); err != nil {
			t.Fatalf("file row %q Path does not exist: %s (%v)", ci.item.Name, ci.item.Path, err)
		}
	}
	sort.Strings(rels)
	want := []string{"SKILL.md", "references/palette.md", "scripts/run.sh"}
	if len(rels) != len(want) {
		t.Fatalf("rels = %v, want %v", rels, want)
	}
	for i, w := range want {
		if rels[i] != w {
			t.Fatalf("rels[%d] = %q, want %q (all=%v)", i, rels[i], w, rels)
		}
	}
}

func TestBuildSkillFileItemsSearchFilter(t *testing.T) {
	dir := writeTestSkillDir(t, "s", map[string]string{
		"SKILL.md":              "x\n",
		"references/palette.md": "p\n",
		"scripts/run.sh":        "r\n",
	})
	items := buildSkillFileItems(dir, "s", "palette")
	// header + 1 match
	if len(items) != 2 {
		t.Fatalf("filtered items = %d, want header + 1", len(items))
	}
	ci, ok := items[1].(cfgItem)
	if !ok || ci.item.Name != "references/palette.md" {
		t.Fatalf("filtered row = %+v, want references/palette.md", items[1])
	}
}

func TestEnterCfgSkillBrowserSwapsList(t *testing.T) {
	app := setupCfgApp(t)
	dir := writeTestSkillDir(t, "my-skill", map[string]string{
		"SKILL.md":              "---\nname: my-skill\n---\nbody\n",
		"references/palette.md": "palette\n",
	})
	skillMD := filepath.Join(dir, "SKILL.md")
	ci := cfgItem{item: session.ConfigItem{Category: session.ConfigSkill, Name: "my-skill", Path: skillMD}}

	app.enterCfgSkillBrowser(ci)
	if !app.cfgSkillBrowse || app.cfgSkillDir != dir || app.cfgSkillName != "my-skill" {
		t.Fatalf("browser state wrong: browse=%v dir=%q name=%q", app.cfgSkillBrowse, app.cfgSkillDir, app.cfgSkillName)
	}
	// List should contain the header + 2 files.
	if got := len(app.cfgList.Items()); got != 3 {
		t.Fatalf("cfgList items = %d, want header + 2", got)
	}
}

func TestHandleCfgEnterSkillEntersBrowser(t *testing.T) {
	app := setupCfgApp(t)
	dir := writeTestSkillDir(t, "my-skill", map[string]string{"SKILL.md": "body\n"})
	skillMD := filepath.Join(dir, "SKILL.md")
	// Put the skill item into the cfgList so SelectedItem returns it.
	items := []list.Item{
		cfgItem{isHeader: true, label: "SKILLS"},
		cfgItem{item: session.ConfigItem{Category: session.ConfigSkill, Name: "my-skill", Path: skillMD}},
	}
	app.cfgList = newConfigList(items, 80, 40)
	app.applyCfgDelegate()
	app.cfgList.Select(1)

	app.handleCfgEnter()
	if !app.cfgSkillBrowse {
		t.Fatal("handleCfgEnter on a skill should enter the browser")
	}
}

func TestHandleCfgEnterInBrowserOpensEditor(t *testing.T) {
	app := setupCfgApp(t)
	dir := writeTestSkillDir(t, "my-skill", map[string]string{
		"SKILL.md":              "body\n",
		"references/palette.md": "palette\n",
	})
	app.enterCfgSkillBrowser(cfgItem{item: session.ConfigItem{Category: session.ConfigSkill, Name: "my-skill", Path: filepath.Join(dir, "SKILL.md")}})
	// Select references/palette.md (skip header).
	for i, it := range app.cfgList.Items() {
		ci, ok := it.(cfgItem)
		if ok && ci.item.Name == "references/palette.md" {
			app.cfgList.Select(i)
			break
		}
	}
	t.Setenv("EDITOR", "true")
	m, cmd := app.handleCfgEnter()
	if m == nil {
		t.Fatal("handleCfgEnter in browser should return a model")
	}
	if cmd == nil {
		t.Fatal("handleCfgEnter in browser should return an editor Cmd")
	}
}

func TestExitCfgSkillBrowserRestores(t *testing.T) {
	app := setupCfgApp(t)
	dir := writeTestSkillDir(t, "my-skill", map[string]string{"SKILL.md": "x\n"})
	app.enterCfgSkillBrowser(cfgItem{item: session.ConfigItem{Category: session.ConfigSkill, Name: "my-skill", Path: filepath.Join(dir, "SKILL.md")}})
	if !app.cfgSkillBrowse {
		t.Fatal("expected to be in browser")
	}
	app.exitCfgSkillBrowser()
	if app.cfgSkillBrowse {
		t.Fatal("exitCfgSkillBrowser should clear cfgSkillBrowse")
	}
	if app.cfgSkillDir != "" {
		t.Fatalf("cfgSkillDir = %q, want empty", app.cfgSkillDir)
	}
}

func TestEscInBrowserClearsSearchThenExits(t *testing.T) {
	app := setupCfgApp(t)
	dir := writeTestSkillDir(t, "my-skill", map[string]string{"SKILL.md": "x\n"})
	app.enterCfgSkillBrowser(cfgItem{item: session.ConfigItem{Category: session.ConfigSkill, Name: "my-skill", Path: filepath.Join(dir, "SKILL.md")}})
	app.cfgSearchTerm = "SKILL"

	// First Esc: search active → clear search, stay in browser.
	app.handleConfigKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if !app.cfgSkillBrowse {
		t.Fatal("first Esc should stay in browser when search is active")
	}
	if app.cfgSearchTerm != "" {
		t.Fatalf("first Esc should clear search, got %q", app.cfgSearchTerm)
	}
	// Second Esc: no search → exit browser.
	app.handleConfigKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if app.cfgSkillBrowse {
		t.Fatal("second Esc should exit the browser")
	}
}
