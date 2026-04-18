package session

import (
	"strings"
	"testing"
)

func TestFilterValueFor_Basics(t *testing.T) {
	s := Session{
		ID:          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ShortID:     "aaaaaaaa",
		ProjectPath: "/Users/edgar/code/ccx",
		ProjectName: "ccx",
		GitBranch:   "main",
		FirstPrompt: "hello world",
		IsLive:      true,
		HasMemory:   true,
	}
	fv := FilterValueFor(s, nil)
	mustContain(t, fv, "ccx", "main", "hello world", "is:live", "has:mem", "proj:ccx")
	mustNotContain(t, fv, "is:busy", "is:current", "is:wt")
}

func TestFilterValueFor_AllFlags(t *testing.T) {
	s := Session{
		ProjectName:     "p",
		TmuxWindowName:  "work",
		IsLive:          true,
		IsResponding:    true,
		IsWorktree:      true,
		HasMemory:       true,
		HasTodos:        true,
		HasTasks:        true,
		HasPlan:         true,
		HasAgents:       true,
		HasCompaction:   true,
		HasSkills:       true,
		HasMCP:          true,
		TeamName:        "squad",
		TeammateName:    "bob",
		ParentSessionID: "parent",
		CustomBadges:    []string{"tag1"},
		IsRemote:        true,
		RemotePodName:   "pod-x",
		RemoteStatus:    "running",
	}
	fv := FilterValueFor(s, nil)
	mustContain(t, fv,
		"win:work", "is:live", "is:busy", "is:wt",
		"has:mem", "has:todo", "has:task", "has:plan",
		"has:agent", "has:compact", "has:skill", "has:mcp",
		"is:team", "team:squad", "bob", "is:fork",
		"tag:tag1", "tag1", "is:remote", "pod-x", "running",
	)
}

func TestFilterValueFor_IsCurrent(t *testing.T) {
	s := Session{ProjectPath: "/tmp/proj-a"}
	if got := FilterValueFor(s, nil); strings.Contains(got, "is:current") {
		t.Fatalf("expected no is:current without cwdPaths; got %q", got)
	}
	if got := FilterValueFor(s, []string{"/tmp/proj-b"}); strings.Contains(got, "is:current") {
		t.Fatalf("expected no is:current for mismatched cwdPath; got %q", got)
	}
	if got := FilterValueFor(s, []string{"/tmp/proj-a"}); !strings.Contains(got, "is:current") {
		t.Fatalf("expected is:current for matching cwdPath; got %q", got)
	}
}

func TestMatches(t *testing.T) {
	fv := "ccx proj:ccx is:live has:mem branch-name"
	cases := []struct {
		q    string
		want bool
	}{
		{"", true},
		{"ccx", true},
		{"CCX", true}, // case-insensitive
		{"is:live", true},
		{"is:live has:mem", true},
		{"is:live is:busy", false},
		{"proj:ccx branch", true},
		{"nomatch", false},
	}
	for _, c := range cases {
		if got := Matches(fv, c.q); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", fv, c.q, got, c.want)
		}
	}
}

func mustContain(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("expected %q to contain %q", haystack, n)
		}
	}
}

func mustNotContain(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("expected %q to NOT contain %q", haystack, n)
		}
	}
}
