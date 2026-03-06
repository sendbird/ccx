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

type ContentBlock struct {
	Type      string
	Text      string
	ToolName  string
	ToolInput string
	IsError   bool
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
