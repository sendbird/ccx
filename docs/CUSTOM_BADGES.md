# Custom Badges

User-created badge labels for organizing and filtering Claude Code sessions.

## Overview

Custom badges let you tag sessions with your own labels for better organization. Create badges like "urgent", "review", or "bug-fix", assign them to sessions, and filter your session list.

## Usage

### Creating & Assigning Badges

**Single Session:**
1. Navigate to a session
2. Press `x` (actions menu)
3. Press `t` (tags)
4. Type badge name in input field
5. Press `Enter` to create and assign
6. Press `Esc` to close

**Multiple Sessions:**
1. Select sessions with `Space` (multi-select)
2. Press `x` → `t`
3. Tag menu shows "Manage Tags (N sessions)"
4. Create or toggle badges - applies to all selected
5. Press `Esc` (automatically clears selection)

**Toggling Existing Badges:**
1. Open tag menu (`x` → `t`)
2. Navigate to badge with `↑`/`↓` or `j`/`k`
3. Press `Enter` to toggle on/off
4. `[✓]` = session has badge, `[ ]` = doesn't have it

### Filtering & Search

**Session List Filter:**
```bash
/                    # Open search
tag:urgent           # Filter by badge
urgent               # Also works without prefix
is:live tag:bug      # Combine with other filters
```

**Search Hint:**
Press `/` to see available filter options including `tag:`

### Deleting Badges

Remove a badge from all sessions:
```bash
:badge:rm urgent
```

Shows confirmation: "Removed 'urgent' from 5 session(s)"

### Display

Custom badges appear after built-in badges in lime green italic:

```
[LIVE] [M] [T] [urgent] [bug-fix]
       ↑      ↑  ↑ custom badges (italic)
       built-in
```

### Keyboard Shortcuts

**In Tag Menu:**
- `↑`/`↓` or `j`/`k` - Navigate badge list
- `i` - Focus input field to type
- `Enter` - Toggle badge (list) or create (input)
- `Esc` - Close menu

**Navigation Flow:**
- Menu opens with input focused (ready to type)
- Press `↓` → moves to list
- Press `i` → returns to input
- Circular navigation: down at bottom wraps to top

## Implementation Details

### Architecture

**Data Model:**
```go
// Session struct
type Session struct {
    // ... existing fields
    CustomBadges []string
}

// Badge storage
type BadgeStore struct {
    mapping map[string][]string // sessionID → badge names
}
```

**Storage Location:**
- File: `~/.claude/badges.json`
- Format: `{"session-uuid": ["urgent", "review"]}`

### Data Flow

**1. Startup - Loading Badges**
```
App starts
  → BadgeStore.LoadBadges(~/.claude)
  → Read badges.json
  → Scanner loads sessions
  → For each session: badgeStore.Get(sessionID)
  → Populate Session.CustomBadges
```

**2. Tagging - Assigning Badge**
```
User presses x → t
  → Tag menu renders (shows BadgeStore.AllBadges())
  → User creates/toggles badge
  → Update Session.CustomBadges array
  → BadgeStore.Set(sessionID, badges)
  → Save to badges.json
  → Re-render session list
```

**3. Display - Rendering Badges**
```
Session list render
  → For each session
  → Render built-in badges [LIVE] [M] [T]
  → Loop through Session.CustomBadges
  → Render with customBadgeStyle (lime green italic)
```

**4. Filtering - Search by Tag**
```
User types: tag:urgent
  → sessionItem.FilterValue() builds searchable string
  → Includes "tag:urgent" and "urgent" for each custom badge
  → List filters using substring match
  → Only matching sessions shown
```

### File Structure

**New Files:**
- `internal/session/badges.go` - BadgeStore with CRUD operations
- `internal/tui/tag_menu.go` - Tag management UI

**Modified Files:**
- `internal/session/models.go` - Added CustomBadges field
- `internal/session/scanner*.go` - Badge loading integration
- `internal/tui/app.go` - Tag menu state + actions integration
- `internal/tui/sessions.go` - Rendering + FilterValue extension
- `internal/tui/styles.go` - Custom badge style
- `internal/tui/keymap.go` - Tags key in actions
- `internal/tui/cmdmode.go` - :badge:rm command

### Key Functions

**BadgeStore (internal/session/badges.go):**
- `LoadBadges(home)` - Initialize from JSON
- `Get(sessionID)` - Retrieve badges for session
- `Set(sessionID, badges)` - Update session badges
- `AllBadges()` - List all distinct badges
- `RemoveBadgeFromAll(name)` - Delete badge globally
- `CountBadgeUsage(name)` - Usage stats
- `Save()` - Persist to disk

**Tag Menu (internal/tui/tag_menu.go):**
- `renderTagMenu()` - Modal UI with badge list + input
- `handleTagMenuKey()` - Navigation and toggle logic
- `updateSessionBadges()` - Update session list after changes
- `validateBadgeName()` - Alphanumeric + dash/underscore only

**Rendering (internal/tui/sessions.go):**
- `sessionDelegate.Render()` - Appends custom badges after built-in
- `FilterValue()` - Includes "tag:name" for each custom badge

### Limits & Validation

**Badge Names:**
- Max 20 characters
- Alphanumeric + dash + underscore only
- No spaces allowed
- Case-preserving (stored as typed)

**Limits:**
- Max 10 badges per session
- Max 100 unique badges globally

### Persistence

**Format:**
```json
{
  "session-uuid-1": ["urgent", "review", "bug-fix"],
  "session-uuid-2": ["urgent", "feature"]
}
```

**Loading Priority:**
1. badges.json read at startup
2. Badges cached in memory (BadgeStore)
3. Session scan populates CustomBadges field
4. Changes saved immediately to disk

### Edge Cases Handled

1. **Empty JSON** - Starts with empty mapping
2. **Missing file** - Creates on first save
3. **Invalid names** - Silently ignored (no error shown yet)
4. **Max limits** - Silently capped
5. **Concurrent saves** - Mutex protected
6. **Badge deletion** - Only removes from sessions, badge name persists in AllBadges
7. **Multi-select** - Union logic (checkmark if ANY session has badge)
8. **Built-in protection** - :badge:rm only affects custom badges

### Integration Points

**Session Scanner:**
- `scanner.go` loads BadgeStore once, passes to workers
- `scanner_stream.go` calls badgeStore.Get() for each session
- Cached sessions also get badges loaded

**Filter System:**
- Uses existing substringFilter in bubble tea list
- FilterValue includes both "tag:name" and "name" for fuzzy matching
- Works with multi-term AND matching

**Actions Menu:**
- Single-select: shows all actions including tags
- Multi-select: shows reduced menu with tags option
- Tag menu integrates seamlessly with existing action flow

### Performance

- Single file read at startup (~1ms for 100 sessions)
- In-memory lookups during render (O(1) map access)
- Saves only on user action (not on render)
- No impact on session scan speed (badges loaded after JSONL parsing)

## Future Enhancements

- Badge renaming (`:badge:rename old new`)
- Badge usage statistics view
- Export/import badge mappings
- Badge colors/icons customization
- Partial checkmarks in multi-select ([~] for some have it)
