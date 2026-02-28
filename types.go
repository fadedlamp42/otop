// data types shared across the codebase.
//
// processInfo comes from the OS (ps + lsof). sessionInfo comes from
// opencode's sqlite db. the TUI correlates them via the PID-to-session
// algorithm in correlate.go.

package main

// processInfo represents an opencode process found via ps.
type processInfo struct {
	pid           int
	cpuPercent    float64
	memMB         float64
	elapsed       string
	tty           string
	tmuxSession   string // tmux session name this process is running in
	tmuxWindow    string // tmux window name
	cwd           string
	cmdline       string
	sessionID     string // from -s flag in cmdline (tier 1)
	startTimeMS   int64  // from log filename via lsof (tier 2)
	isToolProcess bool   // true for `opencode run` (LSPs, wrappers)
}

// sessionInfo represents a session from opencode's sqlite db.
type sessionInfo struct {
	sessionID         string
	title             string
	directory         string
	projectID         string
	model             string
	agent             string
	messageCount      int
	totalInputTokens  int64
	totalOutputTokens int64
	totalCacheRead    int64
	totalCost         float64
	lastFinish        *string // nil when null in db
	lastMessageRole   string
	lastMessageTime   int64
	timeCreated       int64
	timeUpdated       int64
	roundStartTime    int64
	lastOutput        string
	activeTodos       []todoItem
	version           string
	interactive       bool // false when permission is not null
}

// todoItem represents a single todo from a session's todo list.
type todoItem struct {
	content  string
	status   string // pending, in_progress, completed, cancelled
	priority string // high, medium, low
}

// correlatedSession pairs a process with its resolved session.
type correlatedSession struct {
	process processInfo
	session *sessionInfo
}

// fetchResult holds all data collected in a single refresh cycle.
type fetchResult struct {
	correlated  []correlatedSession
	todayStats  aggStats
	globalStats aggStats
	mcpConfig   map[string]any
}

// aggStats holds aggregate token/message statistics.
type aggStats struct {
	sessionCount int
	messageCount int
	totalInput   int64
	totalOutput  int64
}

// messageDetail holds a single message for the detail view.
type messageDetail struct {
	role        string
	finish      string
	model       string
	tokensIn    int64
	tokensOut   int64
	cacheRead   int64
	timeCreated int64
	textPreview string
}
