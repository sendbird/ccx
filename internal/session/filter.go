package session

import (
	"path/filepath"
	"strings"
)

// FilterValueFor returns the space-separated, lowercased token string used for
// filtering a session. It includes the session's project path, name, branch,
// short ID, first prompt, and various `is:` / `has:` / `proj:` / `team:` /
// `win:` / `tag:` marker tokens.
//
// If cwdProjectPaths is non-empty and includes s.ProjectPath (as absolute paths),
// the token "is:current" is added.
func FilterValueFor(s Session, cwdProjectPaths []string) string {
	parts := []string{
		s.ProjectPath,
		s.ProjectName,
		s.GitBranch,
		s.ShortID,
		s.FirstPrompt,
	}
	if s.ProjectName != "" {
		parts = append(parts, "proj:"+s.ProjectName)
	}
	if s.TmuxWindowName != "" {
		parts = append(parts, "win:"+s.TmuxWindowName, s.TmuxWindowName)
	}
	if s.IsLive {
		parts = append(parts, "is:live")
	}
	if s.IsResponding {
		parts = append(parts, "is:busy")
	}
	if s.IsWorktree {
		parts = append(parts, "is:wt")
	}
	if s.HasMemory {
		parts = append(parts, "has:mem")
	}
	if s.HasTodos {
		parts = append(parts, "has:todo")
	}
	if s.HasTasks {
		parts = append(parts, "has:task")
	}
	if s.HasPlan {
		parts = append(parts, "has:plan")
	}
	if s.HasAgents {
		parts = append(parts, "has:agent")
	}
	if s.HasCompaction {
		parts = append(parts, "has:compact")
	}
	if s.HasSkills {
		parts = append(parts, "has:skill")
	}
	if s.HasMCP {
		parts = append(parts, "has:mcp")
	}
	if s.TeamName != "" {
		parts = append(parts, "is:team", "team:"+s.TeamName)
	}
	if s.TeammateName != "" {
		parts = append(parts, s.TeammateName)
	}
	if s.ParentSessionID != "" {
		parts = append(parts, "is:fork")
	}
	for _, badge := range s.CustomBadges {
		parts = append(parts, "tag:"+badge, badge)
	}
	if s.IsRemote {
		parts = append(parts, "is:remote", "remote", s.RemotePodName, s.RemoteStatus)
	}
	if isCurrent(s.ProjectPath, cwdProjectPaths) {
		parts = append(parts, "is:current")
	}
	return strings.Join(parts, " ")
}

// Matches reports whether filterValue (as produced by FilterValueFor) matches
// query, using space-separated substring-AND semantics. Empty query matches.
func Matches(filterValue, query string) bool {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return true
	}
	lower := strings.ToLower(filterValue)
	for _, t := range terms {
		if !strings.Contains(lower, t) {
			return false
		}
	}
	return true
}

func isCurrent(projectPath string, cwdProjectPaths []string) bool {
	if projectPath == "" || len(cwdProjectPaths) == 0 {
		return false
	}
	abs := AbsPath(projectPath)
	for _, p := range cwdProjectPaths {
		if p == "" {
			continue
		}
		if AbsPath(p) == abs {
			return true
		}
	}
	return false
}

// AbsPath returns filepath.Abs(p), falling back to p when Abs fails.
func AbsPath(p string) string {
	abs, _ := filepath.Abs(p)
	if abs == "" {
		abs = p
	}
	return abs
}
