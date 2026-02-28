// formatting helpers: token counts, durations, status inference.
// no lipgloss dependency — pure data transformations.

package main

import (
	"cmp"
	"fmt"
	"os"
	"strings"
	"time"
)

// -- formatting --

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func formatDuration(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	secs := ms / 1000
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs = secs % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%02ds", mins, secs)
	}
	hours := mins / 60
	mins = mins % 60
	if hours < 24 {
		return fmt.Sprintf("%dh%02dm", hours, mins)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd%dh", days, hours)
}

func shortPath(path string, maxLen int) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(path, home) {
		path = "~" + path[len(home):]
	}
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-(maxLen-3):]
}

// truncOrPad truncates or right-pads a string to exactly width characters.
func truncOrPad(s string, width int) string {
	if len(s) > width {
		return s[:width]
	}
	if len(s) < width {
		return s + strings.Repeat(" ", width-len(s))
	}
	return s
}

// tickerSlice returns a scrolling window into text, subway-sign style.
// if text fits within width, returned as-is (padded). otherwise the
// visible window shifts by one character every rateMS milliseconds.
func tickerSlice(text string, width, rateMS int) string {
	if len(text) <= width {
		return truncOrPad(text, width)
	}
	if rateMS <= 0 {
		return truncOrPad(text, width)
	}
	gap := "   "
	cycle := text + gap
	cycleLen := len(cycle)
	padded := strings.Repeat(cycle, (width/cycleLen)+2)
	offset := int(time.Now().UnixMilli()/int64(rateMS)) % cycleLen
	return padded[offset : offset+width]
}

// columnValue extracts the display string for a column key from a session.
func columnValue(key string, cs correlatedSession) string {
	nowMS := time.Now().UnixMilli()

	if cs.session == nil {
		switch key {
		case "title":
			return cs.process.cmdline
		case "last":
			return cs.process.cwd
		case "status":
			return "no-session"
		case "pid":
			return fmt.Sprintf("%d", cs.process.pid)
		case "tty":
			return cs.process.tty
		case "cpu":
			return fmt.Sprintf("%.1f%%", cs.process.cpuPercent)
		case "mem":
			return fmt.Sprintf("%.0fM", cs.process.memMB)
		}
		return ""
	}

	switch key {
	case "title":
		return cs.session.title
	case "last":
		return cs.session.lastOutput
	case "status":
		return inferStatus(cs.session, cs.process.cpuPercent)
	case "msgs":
		return fmt.Sprintf("%d", cs.session.messageCount)
	case "sid":
		return cs.session.sessionID
	case "pid":
		return fmt.Sprintf("%d", cs.process.pid)
	case "uptime":
		if cs.process.startTimeMS > 0 {
			return formatDuration(nowMS - cs.process.startTimeMS)
		}
		return "-"
	case "round":
		if cs.session.roundStartTime > 0 {
			return formatDuration(nowMS - cs.session.roundStartTime)
		}
		return "-"
	case "cpu":
		return fmt.Sprintf("%.1f%%", cs.process.cpuPercent)
	case "mem":
		return fmt.Sprintf("%.0fM", cs.process.memMB)
	case "ctx":
		return formatTokens(cs.session.totalInputTokens)
	case "out":
		return formatTokens(cs.session.totalOutputTokens)
	case "model":
		return shortModel(cs.session.model)
	case "tty":
		return cs.process.tty
	}
	return ""
}

// -- status inference --

// inferStatus determines what a session is currently doing.
//
// primary signal: finish field on the last assistant message.
// secondary signal: CPU% from ps (>5% = actively working on something
// that hasn't been committed to the db yet).
func inferStatus(session *sessionInfo, cpuPercent float64) string {
	if session == nil {
		return "unknown"
	}
	nowMS := time.Now().UnixMilli()
	ageSeconds := float64(9999)
	if session.lastMessageTime > 0 {
		ageSeconds = float64(nowMS-session.lastMessageTime) / 1000
	}
	cpuActive := cpuPercent > 5.0

	if session.lastMessageRole == "assistant" {
		finish := ""
		if session.lastFinish != nil {
			finish = *session.lastFinish
		}

		if finish == "" {
			if ageSeconds < 120 {
				return "generating"
			}
			if cpuActive {
				return "busy"
			}
			return "stale"
		}
		if finish == "tool-calls" {
			if ageSeconds < 30 {
				return "tool use"
			}
			if cpuActive {
				return "busy"
			}
			return "idle"
		}
		if finish == "stop" {
			if cpuActive {
				return "busy"
			}
			return "idle"
		}
		if finish == "length" {
			return "truncated"
		}
		return "idle"
	}

	if session.lastMessageRole == "user" {
		if cpuActive {
			return "thinking"
		}
		if ageSeconds < 60 {
			return "thinking"
		}
		return "queued"
	}

	return "unknown"
}

// -- sorting --

// compareSessions compares two sessions by the given sort key.
// returns -1, 0, or 1. sessions without a match sort to bottom.
// title is used as a secondary key for stability (prevents bounce
// between refreshes when primary values are equal).
func compareSessions(key string, a, b correlatedSession) int {
	// no-session rows sort to bottom
	aHas, bHas := 0, 0
	if a.session == nil {
		aHas = 1
	}
	if b.session == nil {
		bHas = 1
	}
	if aHas != bHas {
		return cmp.Compare(aHas, bHas)
	}
	if a.session == nil {
		return 0
	}

	nowMS := time.Now().UnixMilli()
	var result int

	switch key {
	case "status":
		result = cmp.Compare(
			inferStatus(a.session, a.process.cpuPercent),
			inferStatus(b.session, b.process.cpuPercent))
	case "title":
		result = cmp.Compare(
			strings.ToLower(a.session.title),
			strings.ToLower(b.session.title))
	case "last":
		result = cmp.Compare(a.session.lastOutput, b.session.lastOutput)
	case "msgs":
		result = cmp.Compare(a.session.messageCount, b.session.messageCount)
	case "sid":
		result = cmp.Compare(a.session.sessionID, b.session.sessionID)
	case "pid":
		result = cmp.Compare(a.process.pid, b.process.pid)
	case "uptime":
		aUp := int64(0)
		if a.process.startTimeMS > 0 {
			aUp = nowMS - a.process.startTimeMS
		}
		bUp := int64(0)
		if b.process.startTimeMS > 0 {
			bUp = nowMS - b.process.startTimeMS
		}
		result = cmp.Compare(aUp, bUp)
	case "round":
		aRound := int64(0)
		if a.session.roundStartTime > 0 {
			aRound = nowMS - a.session.roundStartTime
		}
		bRound := int64(0)
		if b.session.roundStartTime > 0 {
			bRound = nowMS - b.session.roundStartTime
		}
		result = cmp.Compare(aRound, bRound)
	case "cpu":
		result = cmp.Compare(a.process.cpuPercent, b.process.cpuPercent)
	case "mem":
		result = cmp.Compare(a.process.memMB, b.process.memMB)
	case "tokens":
		result = cmp.Compare(a.session.totalInputTokens, b.session.totalInputTokens)
	case "model":
		result = cmp.Compare(a.session.model, b.session.model)
	case "tty":
		result = cmp.Compare(a.process.tty, b.process.tty)
	}

	// secondary sort by title for stability
	if result == 0 {
		result = cmp.Compare(
			strings.ToLower(a.session.title),
			strings.ToLower(b.session.title))
	}
	return result
}
