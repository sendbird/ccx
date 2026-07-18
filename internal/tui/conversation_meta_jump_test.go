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

func TestPlanTouchHistoryShownInRow(t *testing.T) {
	row := planRow("/repo/plan.md", session.PlanData{PlanFilePath: "/repo/plan.md"}, session.TouchHistory{})
	if !strings.Contains(stripANSI(row), "plan.md") {
		t.Errorf("plan row should show basename: %q", row)
	}
}
