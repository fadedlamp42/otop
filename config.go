// paths, constants, and column definitions.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const refreshInterval = 2 * time.Second

// dbPath returns the path to opencode's sqlite database.
// respects XDG_DATA_HOME.
func dbPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "opencode", "opencode.db")
}

// configPath returns the path to opencode's global config.
// respects XDG_CONFIG_HOME.
func configPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "opencode", "opencode.json")
}

// shortModel abbreviates long model names for display.
func shortModel(model string) string {
	if model == "" || model == "?" {
		return "?"
	}
	for _, r := range modelReplacements {
		model = strings.Replace(model, r.old, r.short, 1)
	}
	if len(model) > 16 {
		return model[:16]
	}
	return model
}

var modelReplacements = []struct{ old, short string }{
	{"claude-opus-4-5-20251101", "opus-4.5"},
	{"claude-sonnet-4-5-20250929", "sonnet-4.5"},
	{"claude-opus-4-6", "opus-4.6"},
	{"claude-sonnet-4-6", "sonnet-4.6"},
	{"claude-opus-4-5", "opus-4.5"},
	{"claude-sonnet-4-5", "sonnet-4.5"},
	{"gpt-5.2-codex", "gpt-5.2"},
	{"gpt-4o-mini", "4o-mini"},
	{"antigravity-", "ag/"},
	{"gemini-3-pro", "gem-3p"},
	{"gemini-3-flash", "gem-3f"},
}

// columnDef defines a sortable column with a key and display label.
type columnDef struct {
	key   string
	label string
}

// columns defines the sort cycling order (> and < keys).
// STATUS first because it's the most useful default sort.
var columns = []columnDef{
	{"status", "STATUS"},
	{"title", "TITLE"},
	{"last", "LAST OUTPUT"},
	{"msgs", "MSGS"},
	{"sid", "SID"},
	{"pid", "PID"},
	{"uptime", "UPTIME"},
	{"round", "ROUND"},
	{"cpu", "CPU%"},
	{"mem", "MEM"},
	{"tokens", "CTX/OUT"},
	{"model", "MODEL"},
	{"tty", "TTY"},
}

// grid column widths (content, not including gap)
const (
	colStatus = 10 // "generating" is the longest (10 chars)
	colSID    = 30 // full session IDs are always 30 chars
	colUp     = 8  // "12h34m" fits
	colCPU    = 6  // "25.5%" fits
	colCtx    = 8  // "14.8M" / "232.4K"
	colModel  = 12 // "opus-4.6" / "sonnet-4.5"
	colGap    = 2  // space between columns
)
