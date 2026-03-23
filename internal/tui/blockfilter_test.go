package tui

import (
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestApplyBlockFilterEmpty(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{{Type: "text"}}}
	vis := applyBlockFilter("", entry)
	if vis != nil {
		t.Error("expected nil for empty filter")
	}
}

func TestApplyBlockFilterIsTool(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_result", Text: "ok"},
	}}
	vis := applyBlockFilter("is:tool", entry)
	if vis[0] || !vis[1] || vis[2] {
		t.Errorf("expected [false true false], got %v", vis)
	}
}

func TestApplyBlockFilterIsError(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "tool_result", Text: "ok"},
		{Type: "tool_result", Text: "fail", IsError: true},
	}}
	vis := applyBlockFilter("is:error", entry)
	if vis[0] || !vis[1] {
		t.Errorf("expected [false true], got %v", vis)
	}
}

func TestApplyBlockFilterToolName(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_use", ToolName: "Grep"},
		{Type: "tool_use", ToolName: "Bash"},
	}}
	vis := applyBlockFilter("tool:grep", entry)
	if vis[0] || !vis[1] || vis[2] {
		t.Errorf("expected [false true false], got %v", vis)
	}
}

func TestApplyBlockFilterIsHook(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_use", ToolName: "Edit", Hooks: []session.HookInfo{{Command: "go_vet.py"}}},
	}}
	vis := applyBlockFilter("is:hook", entry)
	if vis[0] || !vis[1] {
		t.Errorf("expected [false true], got %v", vis)
	}
}

func TestApplyBlockFilterNegate(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_result", Text: "ok"},
	}}
	vis := applyBlockFilter("!is:text", entry)
	if vis[0] || !vis[1] || !vis[2] {
		t.Errorf("expected [false true true], got %v", vis)
	}
}

func TestApplyBlockFilterFreeText(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "hello world"},
		{Type: "text", Text: "goodbye"},
		{Type: "tool_use", ToolName: "Bash", ToolInput: `{"command":"echo hello"}`},
	}}
	vis := applyBlockFilter("hello", entry)
	if !vis[0] || vis[1] || !vis[2] {
		t.Errorf("expected [true false true], got %v", vis)
	}
}

func TestApplyBlockFilterMultipleTerms(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_use", ToolName: "Grep"},
		{Type: "text", Text: "hello"},
	}}
	// is:tool AND tool:Read → only Read
	vis := applyBlockFilter("is:tool tool:Read", entry)
	if !vis[0] || vis[1] || vis[2] {
		t.Errorf("expected [true false false], got %v", vis)
	}
}

func TestApplyBlockFilterIsSkill(t *testing.T) {
	entry := session.Entry{Content: []session.ContentBlock{
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_use", ToolName: "Skill", ToolInput: `{"skill":"commit"}`},
	}}
	vis := applyBlockFilter("is:skill", entry)
	if vis[0] || !vis[1] {
		t.Errorf("expected [false true], got %v", vis)
	}
}
