package tui

import (
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

// selectMetaContextRow selects the session-meta context row with the given
// sessionMeta tag, opens it zoomed, and returns the app for chaining.
func selectMetaContextRow(t *testing.T, app *App, meta string) {
	t.Helper()
	for i, item := range app.conv.contextItems {
		if item.sessionMeta == meta {
			app.selectConvContext(i)
			app.conv.split.CacheKey = ""
			app.updateConvPreview()
			return
		}
	}
	t.Fatalf("no %q context row (have %d rows)", meta, len(app.conv.contextItems))
}

func TestFlowSummaryDecisionsAreSelectableAndJump(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")

	targets := app.conv.inspector.MetaTargets
	if len(targets) == 0 {
		t.Fatal("summary row produced no meta targets")
	}
	// At least one decision target must carry an origin UUID to jump to.
	var decisionTarget metaEntryTarget
	found := false
	for _, tg := range targets {
		if tg.kind == metaTargetDecision && tg.messageUUID != "" {
			decisionTarget = tg
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no decision target with origin among %+v", targets)
	}

	// The origin turn must resolve to a real merged turn.
	if _, ok := app.mergedByUUID(decisionTarget.messageUUID); !ok {
		t.Fatalf("decision origin %q does not resolve to a merged turn", decisionTarget.messageUUID)
	}

	// Jumping lands on that turn's conversation row.
	model, _, jumped := app.jumpToMetaTarget(decisionTarget)
	if !jumped {
		t.Fatal("jumpToMetaTarget did not handle a decision target with origin")
	}
	app = model.(*App)
	if !app.conv.inspector.Zoom {
		t.Fatal("decision jump should open the conversation inspector zoomed")
	}
}

func TestTasksPlanRowsAreSelectable(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "tasksplan")

	targets := app.conv.inspector.MetaTargets
	var haveTask, havePlan bool
	for _, tg := range targets {
		if tg.kind == metaTargetTask && tg.taskID == "T1" {
			haveTask = true
		}
		if tg.kind == metaTargetPlan && tg.messageUUID != "" {
			havePlan = true
		}
	}
	if !haveTask {
		t.Errorf("tasksplan row missing selectable task T1: %+v", targets)
	}
	if !havePlan {
		t.Errorf("tasksplan row missing selectable plan with origin: %+v", targets)
	}

	// The rendered preview should show both the task subject and plan file.
	view := stripANSI(app.conv.split.Preview.View())
	if !strings.Contains(view, "build the feature") {
		t.Errorf("tasksplan preview missing task subject: %q", view)
	}
	if !strings.Contains(view, "plan.md") {
		t.Errorf("tasksplan preview missing plan file: %q", view)
	}
}

func TestPinnedPlanEnterOpensDetailEscRestoresAndJumps(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "tasksplan")
	app.conv.split.Focus = true

	planCursor := -1
	for i, target := range app.conv.inspector.MetaTargets {
		if target.kind == metaTargetPlan {
			planCursor = i
			break
		}
	}
	if planCursor < 0 {
		t.Fatal("no plan target in tasks/plan preview")
	}
	app.conv.split.Folds.BlockCursor = planCursor
	originID := app.selectedConversationItemID()

	app = pressKey(app, "enter")
	if app.conv.inspector.MetaPlanDrill == "" {
		t.Fatal("Enter on plan row did not open plan detail")
	}
	if got := app.selectedConversationItemID(); got != originID {
		t.Fatalf("Enter moved to conversation item %q, want pinned item %q", got, originID)
	}
	if detail := stripANSI(app.conv.split.Preview.View()); !strings.Contains(detail, "do the thing") {
		t.Fatalf("plan detail missing stored plan data: %q", detail)
	}

	app = pressKey(app, "esc")
	if app.conv.inspector.MetaPlanDrill != "" {
		t.Fatalf("Esc left plan detail active: %q", app.conv.inspector.MetaPlanDrill)
	}
	if got := app.selectedConversationItemID(); got != originID {
		t.Fatalf("Esc restored item %q, want %q", got, originID)
	}
	if app.conv.split.Folds == nil || app.conv.split.Folds.BlockCursor != planCursor {
		t.Fatalf("Esc restored block cursor %d, want %d", app.conv.split.Folds.BlockCursor, planCursor)
	}

	app = pressKey(app, "enter")
	app = pressKey(app, "J")
	if app.conv.contextActive {
		t.Fatal("J from plan detail did not switch selection to the origin conversation turn")
	}
	if got := app.selectedConversationItemID(); got == originID {
		t.Fatalf("J kept pinned selection %q instead of jumping to origin", got)
	}
}

func TestPlanTouchHistoryShownInRow(t *testing.T) {
	row := planRow("/repo/plan.md", session.PlanData{PlanFilePath: "/repo/plan.md"}, session.TouchHistory{})
	if !strings.Contains(stripANSI(row), "plan.md") {
		t.Errorf("plan row should show basename: %q", row)
	}
}

// TestMetaJumpFallsBackToEntryIndex reproduces the "origin turn not found" bug:
// a decision origin points at a transcript entry that mergeConversationTurns
// folded into a multi-entry turn, so the turn's head UUID differs from the
// origin UUID. Jump must resolve via the entry-index range, not head-UUID
// equality.
func TestMetaJumpFallsBackToEntryIndex(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")

	// Find any decision target and pick the entry index it points at.
	var tgt metaEntryTarget
	found := false
	for _, m := range app.conv.inspector.MetaTargets {
		if m.kind == metaTargetDecision && m.entryIndex >= 0 {
			tgt = m
			found = true
			break
		}
	}
	if !found {
		t.Skip("no decision target with a root-transcript entry index")
	}

	// Corrupt the UUID so only the entry-index fallback can locate the turn.
	tgt.messageUUID = "does-not-exist-uuid"
	model, _, jumped := app.jumpToMetaTarget(tgt)
	if !jumped {
		t.Fatal("jumpToMetaTarget should handle a target with a valid entry index")
	}
	app = model.(*App)
	if !app.conv.inspector.Zoom {
		t.Fatal("entry-index fallback jump should open the inspector zoomed (turn found)")
	}
	if app.copiedMsg == "origin turn not found" {
		t.Fatal("entry-index fallback should have located the origin turn")
	}
}

// TestMetaTargetKindJumpable pins which target kinds may jump. None and cron
// must not, so a bare entry-index of 0 never sends a header/todo/cron row to the
// first turn.
func TestMetaTargetKindJumpable(t *testing.T) {
	jump := map[metaTargetKind]bool{
		metaTargetNone:       false,
		metaTargetMemoryFile: true,
		metaTargetDecision:   true,
		metaTargetTask:       true,
		metaTargetTodo:       true,
		metaTargetPlan:       true,
		metaTargetCron:       false,
	}
	for kind, want := range jump {
		if got := kind.jumpable(); got != want {
			t.Errorf("kind %d jumpable = %v, want %v", kind, got, want)
		}
	}
}

// TestNonTargetRowEnterDoesNotJump guards the reported bug: pressing Enter on a
// non-target session-meta row (header/label, metaTargetNone with entryIndex 0)
// must not fall back to jumping to the first conversation turn.
func TestNonTargetRowEnterDoesNotJump(t *testing.T) {
	// entryIndex 0 with a non-jumpable kind must be rejected outright.
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")
	if _, _, jumped := app.jumpToMetaTarget(metaEntryTarget{kind: metaTargetNone, entryIndex: 0}); jumped {
		t.Fatal("metaTargetNone with entryIndex 0 must not jump")
	}
	if _, _, jumped := app.jumpToMetaTarget(metaEntryTarget{kind: metaTargetCron, entryIndex: 0}); jumped {
		t.Fatal("metaTargetCron must not jump")
	}
}

// TestMetaJumpEscRestoresMetaRow verifies that after Enter-jumping from a
// session-meta decision into a conversation turn, esc returns to the originating
// meta context row rather than the first item.
func TestMetaJumpEscRestoresMetaRow(t *testing.T) {
	app := setupDecisionFlowApp(t)
	selectMetaContextRow(t, app, "summary")
	origin := app.selectedConversationItemID()

	var tgt metaEntryTarget
	found := false
	for _, m := range app.conv.inspector.MetaTargets {
		if m.kind == metaTargetDecision && (m.messageUUID != "" || m.entryIndex >= 0) {
			tgt = m
			found = true
			break
		}
	}
	if !found {
		t.Skip("no jumpable decision target")
	}

	model, _, jumped := app.jumpToMetaTarget(tgt)
	if !jumped {
		t.Fatal("decision jump did not happen")
	}
	app = model.(*App)
	if app.selectedConversationItemID() == origin {
		t.Fatal("jump did not move the selection off the meta row")
	}
	app = pressKey(app, "esc")
	if got := app.selectedConversationItemID(); got != origin {
		t.Fatalf("esc restored %q, want origin meta row %q", got, origin)
	}
}
