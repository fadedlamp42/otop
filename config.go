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
	{"tmux", "TMUX"},
	{"tmuxWin", "WINDOW"},
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

// -- display configuration --
// controls which sections and columns are visible.
// two-line mode ignores the columns config and shows the full layout.
// one-line mode uses the columns config to pick which columns appear.

type displayConfig struct {
	showHeader         bool
	showAggregateStats bool
	showColumnHeaders  bool
	oneLine            bool
	defaultSortKey     string // column key to sort by on startup (e.g. "round", "status")
	defaultSortReverse bool   // true = descending, false = ascending
	columns            columnConfig
	ticker             tickerConfig
}

// columnConfig toggles individual columns in one-line mode.
type columnConfig struct {
	title   bool
	last    bool
	status  bool
	msgs    bool
	sid     bool
	pid     bool
	uptime  bool
	round   bool
	cpu     bool
	mem     bool
	ctx     bool
	out     bool
	model   bool
	tty     bool
	tmux    bool
	tmuxWin bool
}

// tickerConfig controls the subway-style scrolling ticker for the "last" column.
// width sets the fixed character count; rateMS controls scroll speed.
// only applies in one-line mode when the "last" column is enabled.
type tickerConfig struct {
	width  int
	rateMS int
}

// display is the active layout configuration.
// edit these fields to customize the layout.
var display = displayConfig{
	showHeader:         false,
	showAggregateStats: false,
	showColumnHeaders:  false,
	oneLine:            true,
	defaultSortKey:     "round",
	defaultSortReverse: false, // ascending: fresh rounds at top
	columns: columnConfig{
		title:   true,
		last:    true,
		status:  true,
		round:   true,
		model:   true,
		tmux:    true,
		tmuxWin: true,
	},
	ticker: tickerConfig{
		width:  0, // 0 = flexible, fills remaining space. >0 = fixed character count.
		rateMS: 300,
	},
}

// -- full layout preset (uncomment to switch) --
// var display = displayConfig{
// 	showHeader:         true,
// 	showAggregateStats: true,
// 	showColumnHeaders:  true,
// 	oneLine:            false,
// 	columns: columnConfig{
// 		title: true, last: true, status: true, msgs: true,
// 		sid: true, pid: true, uptime: true, round: true,
// 		cpu: true, mem: true, ctx: true, out: true,
// 		model: true, tty: true,
// 	},
// 	ticker: tickerConfig{width: 0, rateMS: 300},
// }

func (c columnConfig) isEnabled(key string) bool {
	switch key {
	case "title":
		return c.title
	case "last":
		return c.last
	case "status":
		return c.status
	case "msgs":
		return c.msgs
	case "sid":
		return c.sid
	case "pid":
		return c.pid
	case "uptime":
		return c.uptime
	case "round":
		return c.round
	case "cpu":
		return c.cpu
	case "mem":
		return c.mem
	case "ctx":
		return c.ctx
	case "out":
		return c.out
	case "model":
		return c.model
	case "tty":
		return c.tty
	case "tmux":
		return c.tmux
	case "tmuxWin":
		return c.tmuxWin
	}
	return false
}

// oneLineColSpec describes a column in one-line mode.
type oneLineColSpec struct {
	key   string
	label string
	width int // 0 = flexible, takes remaining space
}

// oneLineColumnOrder defines display order and base widths for one-line mode.
var oneLineColumnOrder = []oneLineColSpec{
	{"tmux", "TMUX", 12},
	{"tmuxWin", "WINDOW", 12},
	{"sid", "SID", 30},
	{"title", "TITLE", 0},
	{"last", "LAST", 0},
	{"status", "STATUS", 10},
	{"msgs", "MSGS", 5},
	{"pid", "PID", 8},
	{"uptime", "UP", 8},
	{"round", "ROUND", 8},
	{"cpu", "CPU", 6},
	{"mem", "MEM", 6},
	{"ctx", "CTX", 8},
	{"out", "OUT", 8},
	{"model", "MODEL", 12},
	{"tty", "TTY", 12},
}

// enabledOneLineColumns returns the enabled columns with widths resolved.
// the "last" column width comes from ticker.width when set.
func enabledOneLineColumns() []oneLineColSpec {
	var result []oneLineColSpec
	for _, col := range oneLineColumnOrder {
		if !display.columns.isEnabled(col.key) {
			continue
		}
		if col.key == "last" && display.ticker.width > 0 {
			col.width = display.ticker.width
		}
		result = append(result, col)
	}
	return result
}
