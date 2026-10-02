package tui

import (
	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// writeHeavySession lays down a transcript with a subagent, a background shell
// and a scratchpad file, so every off-loop preview mode has something to load.
func writeHeavySession(t *testing.T, claudeDir, project, id string) session.Session {
	t.Helper()
	projDir := filepath.Join(claudeDir, "projects", project)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	var sb strings.Builder
	sb.WriteString(line(map[string]any{
		"type": "user", "sessionId": id, "cwd": "/tmp/" + project,
		"message": map[string]any{"role": "user", "content": "hello"},
	}))
	sb.WriteString(line(map[string]any{
		"type": "assistant", "sessionId": id,
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Task",
				"input": map[string]any{"description": "probe", "prompt": "go"}},
			map[string]any{"type": "tool_use", "id": "t2", "name": "Bash",
				"input": map[string]any{"command": "sleep 1", "run_in_background": true}},
		}},
	}))
	file := filepath.Join(projDir, id+".jsonl")
	if err := os.WriteFile(file, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return session.Session{
		ID: id, FilePath: file, ProjectPath: projDir,
		ModTime: time.Now(), HasAgents: true, HasShellJobs: true,
	}
}

// writeScratchpadFile lays down one scratchpad file for sess under a temporary
// scratchpad base, and returns what LoadScratchpadFiles then finds.
func writeScratchpadFile(t *testing.T, sess session.Session, name, body string) []session.ScratchpadFile {
	t.Helper()
	base := t.TempDir()
	t.Cleanup(session.SetScratchpadBaseOverride(base))
	dir := filepath.Join(base, session.EncodeProjectPath(sess.ProjectPath), sess.ID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	files := session.LoadScratchpadFiles(sess.ProjectPath, sess.ID)
	if len(files) == 0 {
		t.Fatalf("fixture produced no scratchpad files under %s", dir)
	}
	return files
}

// offLoopPreviewModes is the set this test covers; it must track
// previewLoadsOffLoop so a new off-loop mode cannot slip in uncovered.
var offLoopPreviewModes = []struct {
	name string
	mode sessPreview
}{
	{"contexts", sessPreviewContexts},
	{"scratchpad", sessPreviewScratchpad},
	{"shells", sessPreviewShells},
	{"agents", sessPreviewAgents},
	{"workflows", sessPreviewWorkflows},
}

func TestOffLoopPreviewModesMatchCoverage(t *testing.T) {
	covered := map[sessPreview]bool{}
	for _, m := range offLoopPreviewModes {
		covered[m.mode] = true
		if !previewLoadsOffLoop(m.mode) {
			t.Errorf("%s is covered here but previewLoadsOffLoop says it is inline", m.name)
		}
	}
	// Walk the whole preview-mode range so a mode added to previewLoadsOffLoop
	// without a case in previewLoader (or coverage here) fails loudly.
	for mode := sessPreview(0); mode < numSessPreviewModes; mode++ {
		if previewLoadsOffLoop(mode) && !covered[mode] {
			t.Errorf("preview mode %d loads off-loop but has no coverage here", mode)
		}
	}
}

// TestOffLoopPreviewDoesNotTouchDiskInView is the regression guard. Before this
// change the heavy preview builders ran inline, so whichever of Update or View
// reached them first read and JSON-parsed a transcript on the UI loop —
// measured at 49.7ms median and 722ms worst case for the contexts pane, which
// is what made holding j/k feel frozen.
//
// Asserting a wall-clock number would go flaky under load, so this asserts the
// structural property instead: with the load not yet delivered, View must leave
// the mode's payload empty (it renders a placeholder), and the command the
// update returned must be what fills it.
func TestOffLoopPreviewDoesNotTouchDiskInView(t *testing.T) {
	claudeDir := t.TempDir()
	sess := writeHeavySession(t, claudeDir, "proj-a", "sess-a")

	for _, tc := range offLoopPreviewModes {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp([]session.Session{sess})
			app.config.ClaudeDir = claudeDir
			app.sessSplit.Show = true
			app.sessPreviewMode = tc.mode
			app.sessSplit.CacheKey = ""

			cmd := app.updateSessionPreviewWith(true)
			if cmd == nil {
				t.Fatalf("%s: no load command dispatched; the build would be inline", tc.name)
			}
			key := previewLoadKey(tc.mode, sess)
			if !app.previewLoadInFlight[key] {
				t.Fatalf("%s: load not marked in flight, so a repeat visit would dispatch again", tc.name)
			}
			if _, ok := app.previewLoads.Get(key); ok {
				t.Fatalf("%s: payload cached before the command ran — the load happened inline", tc.name)
			}

			// View must not complete the load behind the command's back.
			_ = app.View()
			if _, ok := app.previewLoads.Get(key); ok {
				t.Fatalf("%s: View() populated the cache, so it did the disk work on the UI loop", tc.name)
			}

			msg, ok := cmd().(previewLoadMsg)
			if !ok {
				t.Fatalf("%s: command returned %T, want previewLoadMsg", tc.name, cmd())
			}
			if msg.dataKey != key || msg.mode != tc.mode {
				t.Fatalf("%s: msg targets %q/%d, want %q/%d", tc.name, msg.dataKey, msg.mode, key, tc.mode)
			}
			if !app.applyPreviewLoad(tc.mode, sess, msg.payload) {
				t.Fatalf("%s: applyPreviewLoad rejected payload %T", tc.name, msg.payload)
			}
		})
	}
}

// TestOffLoopPreviewRevisitReusesLoad covers the other half: the per-mode App
// fields track one session, so a row change clears them. Without adopting the
// cached payload first, moving the cursor off a row and back would re-read a
// transcript already on hand.
func TestOffLoopPreviewRevisitReusesLoad(t *testing.T) {
	claudeDir := t.TempDir()
	a := writeHeavySession(t, claudeDir, "proj-a", "sess-a")
	b := writeHeavySession(t, claudeDir, "proj-b", "sess-b")

	for _, tc := range offLoopPreviewModes {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp([]session.Session{a, b})
			app.config.ClaudeDir = claudeDir
			app.sessSplit.Show = true
			app.sessPreviewMode = tc.mode

			app.sessGroupMode = groupFlat
			app.rebuildSessionList()

			// Count the loads that actually ran, draining every command the
			// way the runtime does — handlePreviewLoad re-renders and can
			// return a command of its own, and a dropped one would leave
			// previewLoadInFlight set and hide a missing cache.
			loads := 0
			seen := map[string]bool{}
			var drain func(cmd tea.Cmd, depth int)
			drain = func(cmd tea.Cmd, depth int) {
				if cmd == nil || depth > 8 {
					return
				}
				msg := cmd()
				lm, ok := msg.(previewLoadMsg)
				if !ok {
					return
				}
				loads++
				drain(app.handlePreviewLoad(lm), depth+1)
			}
			visit := func(idx int) {
				app.sessionList.Select(idx)
				app.sessSplit.CacheKey = ""
				sel, ok := app.selectedSession()
				if !ok {
					t.Fatalf("%s: row %d is not a session row", tc.name, idx)
				}
				seen[sel.ID] = true
				drain(app.updateSessionPreviewWith(true), 0)
			}
			// a → b → a → b: two sessions, so two loads and no more.
			visit(0)
			visit(1)
			visit(0)
			visit(1)
			if len(seen) != 2 {
				t.Fatalf("%s: the four visits only reached %d distinct session(s); the test would pass without caching", tc.name, len(seen))
			}
			if loads != 2 {
				t.Fatalf("%s: ran %d loads for 2 sessions over 4 visits; revisits are re-reading disk", tc.name, loads)
			}
		})
	}
}

// TestOffLoopPreviewRendersAfterLoad makes sure the async path actually ends in
// content: a measurement showing "no work on the loop" would look identical if
// the pane were stuck on its placeholder forever.
func TestOffLoopPreviewRendersAfterLoad(t *testing.T) {
	claudeDir := t.TempDir()
	sess := writeHeavySession(t, claudeDir, "proj-a", "sess-a")
	if err := os.MkdirAll(filepath.Join(sess.ProjectPath, "scratchpad"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range offLoopPreviewModes {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp([]session.Session{sess})
			app.config.ClaudeDir = claudeDir
			app.sessSplit.Show = true
			app.sessPreviewMode = tc.mode
			app.sessSplit.CacheKey = ""

			// Drain the load, then the re-render it triggers.
			for range 4 {
				cmd := app.updateSessionPreviewWith(true)
				if cmd == nil {
					break
				}
				if msg, ok := cmd().(previewLoadMsg); ok {
					app.handlePreviewLoad(msg)
				}
				app.sessSplit.CacheKey = ""
			}
			got := app.sessSplit.Preview.View()
			if strings.Contains(got, "loading") {
				t.Fatalf("%s: pane still on its loading placeholder after the load landed:\n%s", tc.name, got)
			}
			if strings.TrimSpace(stripANSI(got)) == "" {
				t.Fatalf("%s: pane is empty after the load landed", tc.name)
			}
		})
	}
}

// TestInspectorScratchpadLoadsItsOwnFiles pins the other caller of
// buildScratchpadContent. The sessions pane hands it files fetched off the UI
// loop; the conversation inspector is reached by selecting a node, not by row
// navigation, so it loads them itself. Making the builder read the pane's
// cached slice instead of taking a parameter silently emptied this view.
func TestInspectorScratchpadLoadsItsOwnFiles(t *testing.T) {
	claudeDir := t.TempDir()
	sess := writeHeavySession(t, claudeDir, "proj-a", "sess-a")
	files := writeScratchpadFile(t, sess, "notes.md", "probe-marker\n")

	app := newTestApp([]session.Session{sess})
	app.config.ClaudeDir = claudeDir
	// The pane's cache is deliberately empty: the inspector must not depend on it.
	app.sessScratchpadFiles = nil

	got := stripANSI(app.buildScratchpadContent(sess, files))
	if strings.Contains(got, "No scratchpad files") {
		t.Fatalf("inspector rendered the empty state for a session that has files:\n%s", got)
	}
}

// TestOutputsDigestSurvivesRowChange guards the digest cache. The App tracks one
// session's outputs, so a row change clears it and the collection handler used
// to drop a result that landed after the cursor moved — making a walk up and
// down the list rescan each transcript, at 8ms median and 117ms p95 apiece.
func TestOutputsDigestSurvivesRowChange(t *testing.T) {
	claudeDir := t.TempDir()
	a := writeHeavySession(t, claudeDir, "proj-a", "sess-a")
	b := writeHeavySession(t, claudeDir, "proj-b", "sess-b")

	app := newTestApp([]session.Session{a, b})
	app.config.ClaudeDir = claudeDir
	app.sessSplit.Show = true
	app.sessPreviewMode = sessPreviewOutputs
	app.sessGroupMode = groupFlat
	app.rebuildSessionList()

	collected := map[string]int{}
	visit := func(idx int) {
		app.sessionList.Select(idx)
		app.sessSplit.CacheKey = ""
		cmd := app.updateSessionPreviewWith(true)
		if cmd == nil {
			return
		}
		msg, ok := cmd().(outputsCollectedMsg)
		if !ok {
			return
		}
		collected[msg.id]++
		m, _ := app.Update(msg)
		app = m.(*App)
	}
	// a → b → a → b: each transcript must be scanned exactly once.
	visit(0)
	visit(1)
	visit(0)
	visit(1)
	for id, n := range collected {
		if n != 1 {
			t.Fatalf("collected %s %d times over two visits; the digest is not surviving a row change", id, n)
		}
	}
	if len(collected) != 2 {
		t.Fatalf("collected %d sessions, want 2", len(collected))
	}
}
