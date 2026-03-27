package session

import "time"

type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, completed
}

type TaskItem struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Status      string   `json:"status"`
	Description string   `json:"description"`
	ActiveForm  string   `json:"activeForm"`
	Blocks      []string `json:"blocks"`
	BlockedBy   []string `json:"blockedBy"`
}

type Session struct {
	ID          string
	ShortID     string
	FilePath    string
	ProjectPath string
	ProjectName string
	GitBranch   string
	ModTime     time.Time
	MsgCount    int
	FirstPrompt string
	Created     time.Time
	IsWorktree  bool
	IsLive       bool
	IsResponding bool
	HasMemory   bool
	HasTodos    bool
	Todos       []TodoItem
	HasTasks    bool
	HasPlan     bool
	PlanSlug    string   // first plan slug (kept for compat)
	PlanSlugs   []string // all distinct plan slugs in order
	Tasks       []TaskItem
	TeamName     string // e.g. "supports-build"
	TeamRole     string // "leader", "teammate", ""
	TeammateName string // e.g. "build-deploy" (teammate only)

	ParentSessionID string // UUID of parent session (empty if not a fork)

	HasAgents     bool
	HasCompaction bool
	HasSkills     bool
	HasMCP        bool

	CustomBadges []string // user-created badge tags

	TmuxWindowName string // tmux window name (set if pane CWD matches ProjectPath)
}

type Entry struct {
	Type      string
	Timestamp time.Time
	IsMeta    bool
	Role      string
	Content   []ContentBlock
	Model     string
	UUID      string
	ParentID  string
	AgentID   string
	RawJSON   string
}

type HookInfo struct {
	Event   string // "PreToolUse", "PostToolUse", "Stop"
	Name    string // "PostToolUse:Read"
	Command string // "uv run ~/.claude/hooks/go_vet.py"
	// Note: UserPromptSubmit and Notification hooks are NOT recorded as
	// hook_progress entries in Claude Code's JSONL. Only PreToolUse,
	// PostToolUse, and Stop events generate hook_progress entries.
}

type ContentBlock struct {
	Type      string
	Text      string
	ToolName  string
	ToolInput string
	IsError   bool
	ID        string     // tool_use block ID (e.g., "toolu_01...")
	Hooks     []HookInfo // hooks that ran for this tool_use block
	TagName      string     // for system_tag blocks: the XML tag name (e.g., "system-reminder")
	ImagePasteID int        // for image blocks: the paste ID for cache lookup (0 = not set)
}

type Subagent struct {
	ID          string
	ShortID     string
	FilePath    string
	MsgCount    int
	FirstPrompt string
	Timestamp   time.Time
	AgentType   string
}
