package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func benchmarkSessions(n int) []session.Session {
	now := time.Now()
	sessions := make([]session.Session, 0, n)
	for i := 0; i < n; i++ {
		project := i % 40
		isWorktree := i%3 == 0
		base := fmt.Sprintf("/tmp/repo-%02d", project)
		projectPath := base
		projectName := fmt.Sprintf("repo-%02d", project)
		if isWorktree {
			projectPath = fmt.Sprintf("%s/.worktree/branch-%03d", base, i)
			projectName = fmt.Sprintf("branch-%03d", i)
		}
		sessions = append(sessions, session.Session{
			ID:             fmt.Sprintf("session-%04d", i),
			ShortID:        fmt.Sprintf("%08d", i),
			ProjectPath:    projectPath,
			ProjectName:    projectName,
			GitBranch:      fmt.Sprintf("branch-%02d", i%9),
			ModTime:        now.Add(-time.Duration(i) * time.Minute),
			MsgCount:       10 + i%300,
			FirstPrompt:    fmt.Sprintf("Investigate project %02d issue %03d and summarize the findings", project, i),
			IsWorktree:     isWorktree,
			IsLive:         i%7 == 0,
			HasShellJobs:   i%11 == 0,
			HasMonitorJobs: i%17 == 0,
			HasTasks:       i%5 == 0,
			HasAgents:      i%4 == 0,
		})
	}
	return sessions
}

func BenchmarkProjectCentricBuildGroupedItems(b *testing.B) {
	sessions := benchmarkSessions(1000)
	folded := map[string]bool{
		"repo:/tmp/repo-01": true,
		"repo:/tmp/repo-02": true,
		"repo:/tmp/repo-03": true,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		items := buildGroupedItems(sessions, groupProjectCentric, folded, ".worktree")
		if len(items) == 0 {
			b.Fatal("expected items")
		}
	}
}

func BenchmarkProjectCentricListViewColdCache(b *testing.B) {
	sessions := benchmarkSessions(1000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cache := newSessionRowCache(2048)
		l := newSessionList(sessions, 120, 40, groupProjectCentric, nil, nil, nil, cache, ".worktree")
		if v := l.View(); v == "" {
			b.Fatal("empty view")
		}
	}
}

func BenchmarkProjectCentricListViewWarmCache(b *testing.B) {
	sessions := benchmarkSessions(1000)
	cache := newSessionRowCache(2048)
	l := newSessionList(sessions, 120, 40, groupProjectCentric, nil, nil, nil, cache, ".worktree")
	_ = l.View()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v := l.View(); v == "" {
			b.Fatal("empty view")
		}
	}
}

func benchmarkMergedMessages(n int) []mergedMsg {
	now := time.Now()
	msgs := make([]mergedMsg, 0, n)
	for i := 0; i < n; i++ {
		role := "assistant"
		if i%3 == 0 {
			role = "user"
		}
		text := strings.Repeat(fmt.Sprintf("message-%04d project investigation detail ", i), 4)
		blocks := []session.ContentBlock{{Type: "text", Text: text}}
		if i%5 == 0 {
			blocks = append(blocks, session.ContentBlock{Type: "tool_use", ToolName: "Bash", ToolInput: `{"command":"go test ./..."}`})
		}
		msgs = append(msgs, mergedMsg{
			entry: session.Entry{
				UUID:      fmt.Sprintf("uuid-%04d", i),
				Role:      role,
				Timestamp: now.Add(time.Duration(i) * time.Minute),
				Content:   blocks,
			},
			startIdx: i,
			endIdx:   i,
		})
	}
	return msgs
}

func BenchmarkConversationPreviewColdCache(b *testing.B) {
	msgs := benchmarkMergedMessages(1000)
	expanded := make(map[int]bool)
	for i := 0; i < len(msgs); i += 20 {
		expanded[i] = true
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cache := newSessionRowCache(4096)
		if v := renderConversationPreview(msgs, 120, 10, expanded, "", cache); v == "" {
			b.Fatal("empty preview")
		}
	}
}

func BenchmarkConversationPreviewWarmCache(b *testing.B) {
	msgs := benchmarkMergedMessages(1000)
	expanded := make(map[int]bool)
	for i := 0; i < len(msgs); i += 20 {
		expanded[i] = true
	}
	cache := newSessionRowCache(4096)
	_ = renderConversationPreview(msgs, 120, 10, expanded, "", cache)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v := renderConversationPreview(msgs, 120, 10, expanded, "", cache); v == "" {
			b.Fatal("empty preview")
		}
	}
}

func BenchmarkConversationPreviewWindowedWarmCache(b *testing.B) {
	msgs := benchmarkMergedMessages(1000)
	cursor := 500
	expanded := map[int]bool{cursor: true, cursor + 1: true}
	cache := newSessionRowCache(4096)
	_, _, _, _, _ = renderConversationPreviewWindowed(msgs, 120, cursor, expanded, "", cache)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v, _, _, _, _ := renderConversationPreviewWindowed(msgs, 120, cursor, expanded, "", cache); v == "" {
			b.Fatal("empty preview")
		}
	}
}

func BenchmarkProjectSummaryPreviewRender(b *testing.B) {
	sessions := benchmarkSessions(500)
	app := NewApp(sessions, Config{TmuxEnabled: true})
	app.width = 160
	app.height = 50
	app.sessSplit = SplitPane{List: &app.sessionList, ItemHeight: 2, Show: true}
	items := buildGroupedItems(sessions, groupProjectCentric, nil, ".worktree")
	var pi projectItem
	for _, item := range items {
		if p, ok := item.(projectItem); ok {
			pi = p
			break
		}
	}
	if pi.basePath == "" {
		b.Fatal("expected a project item")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		app.updateProjectPreview(pi)
		if app.sessSplit.Preview.View() == "" {
			b.Fatal("empty project preview")
		}
	}
}
