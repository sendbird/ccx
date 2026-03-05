package session

import "sync"

// Byte markers for fast line-level pre-filtering (no JSON parse needed).
// Markers come in pairs: compact ("key":"val") and spaced ("key": "val").
var (
	// Role markers
	bRoleUser  = []byte(`"role":"user"`)
	bRoleUserS = []byte(`"role": "user"`)
	bRoleAsst  = []byte(`"role":"assistant"`)
	bRoleAsstS = []byte(`"role": "assistant"`)

	// Meta / type markers
	bIsMeta        = []byte(`"isMeta":true`)
	bIsMetaSpaced  = []byte(`"isMeta": true`)
	bTypeProgress  = []byte(`"type":"progress"`)
	bTypeProgressS = []byte(`"type": "progress"`)
	bTypeFileHist  = []byte(`"type":"file-history-snapshot"`)
	bTypeFileHistS = []byte(`"type": "file-history-snapshot"`)

	// Metadata fields
	bCwd           = []byte(`"cwd"`)
	bGitBranch     = []byte(`"gitBranch"`)
	bTodosNonEmpty = []byte(`"todos":[{`)
	bSlug          = []byte(`"slug"`)

	// Team/agent markers
	bTeamName   = []byte(`"teamName":"`)
	bTeamNameS  = []byte(`"teamName": "`)
	bAgentName  = []byte(`"agentName":"`)
	bAgentNameS = []byte(`"agentName": "`)

	// Feature detection markers
	bIsCompactSummary  = []byte(`"isCompactSummary":true`)
	bIsCompactSummaryS = []byte(`"isCompactSummary": true`)
	bSkillTool         = []byte(`"name":"Skill"`)
	bSkillToolS        = []byte(`"name": "Skill"`)
	bMCPTool           = []byte(`"name":"mcp__`)
	bMCPToolS          = []byte(`"name": "mcp__`)
	bTaskCreate        = []byte(`"name":"TaskCreate"`)
	bTaskCreateS       = []byte(`"name": "TaskCreate"`)

	// Stats-specific markers
	bUsage     = []byte(`"usage":{`)
	bUsageS    = []byte(`"usage": {`)
	bToolUse   = []byte(`"type":"tool_use"`)
	bToolUseS  = []byte(`"type": "tool_use"`)
	bIsErrorT  = []byte(`"is_error":true`)
	bIsErrorTS = []byte(`"is_error": true`)
	bToolRes   = []byte(`"type":"tool_result"`)
	bToolResS  = []byte(`"type": "tool_result"`)
	bNameQ     = []byte(`"name":"`)
	bNameQS    = []byte(`"name": "`)
	bFilePathQ = []byte(`"file_path":"`)
	bFilePathS = []byte(`"file_path": "`)
	bModelQ    = []byte(`"model":"`)
	bModelQS   = []byte(`"model": "`)
	bSkillQ    = []byte(`"skill":"`)
	bSkillQS   = []byte(`"skill": "`)
	bCmdTag    = []byte(`<command-name>`)
	bCmdTagEnd = []byte(`</command-name>`)
	bIDCol     = []byte(`"id":"`)
	bIDColS    = []byte(`"id": "`)
	bTUIDCol   = []byte(`"tool_use_id":"`)
	bTUIDColS  = []byte(`"tool_use_id": "`)

	// Path decode cache
	decodedPathCache sync.Map // dirName → decoded path (string, "" if unresolvable)
)
