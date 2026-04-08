package session

import "time"

// SessionStats holds aggregated statistics extracted from a session JSONL file.
type SessionStats struct {
	// Token usage
	TotalInputTokens         int64
	TotalOutputTokens        int64
	TotalCacheReadTokens     int64
	TotalCacheCreationTokens int64

	// Per-assistant-message output token counts (chronological, for sparkline)
	OutputTokenSeries []int

	// Tool usage: tool name -> call count
	ToolCounts map[string]int

	// Code activity
	WriteCount   int
	EditCount    int
	ReadCount    int
	BashCount    int
	FilesTouched map[string]bool

	// Errors
	ToolResultCount int
	ToolErrorCount  int
	ToolErrors      map[string]int // tool name -> error count
	SkillErrors     map[string]int // skill name -> error count
	CommandErrors   map[string]int // command name -> error count
	ErrorTimestamps []time.Time    // when errors occurred (for timeline)

	// Per-tool timestamps for timeline sparklines
	ToolCallTimestamps  map[string][]time.Time // tool name -> call timestamps
	ToolErrorTimestamps map[string][]time.Time // tool name -> error timestamps

	// MCP tools: name -> count (subset of ToolCounts for mcp__ prefixed tools)
	MCPToolCounts map[string]int

	// Commands: slash command name -> count (e.g. "/commit" -> 2)
	CommandCounts map[string]int

	// Skills: skill name -> count (from Skill tool_use)
	SkillCounts map[string]int

	// Agents: subagent_type -> count (from Agent tool_use)
	AgentCounts map[string]int

	// Timeline
	FirstTimestamp time.Time
	LastTimestamp  time.Time
	MessageCount   int
	UserMsgCount   int
	AsstMsgCount   int

	// Compaction
	CompactionCount int

	// Turns per user request
	TurnsPerRequest []int

	// Per-model token usage
	ModelTokens map[string]*ModelUsage

	// Model switching
	ModelSwitches int

	// Message timing
	AvgMsgGap time.Duration
	MaxMsgGap time.Duration

	// Per-message timestamps for timeline visualization
	MsgTimestamps []time.Time

	// Models used
	Models map[string]int

	// Hook usage: command -> count, event -> count
	HookCounts        map[string]int // command path -> total invocations
	HookEventCounts   map[string]int // event type ("PreToolUse", "PostToolUse", "Stop") -> count
	HookTimestamps    map[string][]time.Time // command -> timestamps
}

// ModelUsage tracks token counts per model for cost estimation.
type ModelUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// GlobalStats holds aggregated statistics across all sessions.
type GlobalStats struct {
	SessionCount  int
	TotalMessages int
	TotalUserMsgs int
	TotalAsstMsgs int
	TotalDuration time.Duration
	AvgDuration   time.Duration

	TotalInputTokens         int64
	TotalOutputTokens        int64
	TotalCacheReadTokens     int64
	TotalCacheCreationTokens int64

	ToolCounts    map[string]int
	MCPToolCounts map[string]int
	SkillCounts   map[string]int
	CommandCounts map[string]int
	Models        map[string]int

	TotalWrites, TotalEdits, TotalFiles int
	TotalToolResults, TotalToolErrors   int
	TotalCompactions                    int
	SessionsWithCompaction              int

	TotalCostUSD   float64
	ModelTokens    map[string]*ModelUsage

	TotalModelSwitches    int
	SessionsWithSwitches  int

	ToolErrors    map[string]int
	SkillErrors   map[string]int
	CommandErrors map[string]int

	AgentCounts map[string]int // subagent_type -> total count across sessions

	AllTurnsPerRequest []int

	SessionDurations []time.Duration
	SessionTokens    []int64
	SessionStarts    []time.Time
	AllErrorTimestamps []time.Time

	AllToolCallTimestamps  map[string][]time.Time
	AllToolErrorTimestamps map[string][]time.Time

	AllMsgTimestamps []time.Time

	HookCounts      map[string]int            // command -> total invocations
	HookEventCounts map[string]int            // event type -> count
	HookTimestamps  map[string][]time.Time    // command -> timestamps

	ProjectStats []ProjectStats // per-project aggregated stats, sorted by cost desc
}

// ProjectStats holds aggregated stats for a single project.
type ProjectStats struct {
	ProjectName              string
	ProjectPath              string
	SessionCount             int
	TotalInputTokens         int64
	TotalOutputTokens        int64
	TotalCacheReadTokens     int64
	TotalCacheCreationTokens int64
	CostUSD                  float64
	TotalMessages            int
	TotalDuration            time.Duration
}
