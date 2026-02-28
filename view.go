// rendering: the View() method delegates and all list view display.
//
// styles follow stop's visual encoding approach: lipgloss-based ANSI
// colors with a status-driven gradient (green = active, yellow =
// transitional, white = idle, dim = stale, red = error).

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// -- styles (matching stop's visual encoding) --

var (
	// structural
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	panelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	keyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// status colors
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))  // green
	transStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // yellow
	idleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // bright white
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))  // red
	staleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // dim

	// selection + sort highlighting
	selectStyle = lipgloss.NewStyle().Background(lipgloss.Color("6")).Foreground(lipgloss.Color("0"))
	sortHiStyle = lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0")).Bold(true)
	hdrDimBold  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)
)

// statusStyleFor returns the lipgloss style for a given status string.
func statusStyleFor(status string) lipgloss.Style {
	switch status {
	case "generating", "tool use", "busy":
		return activeStyle
	case "thinking", "queued":
		return transStyle
	case "idle":
		return idleStyle
	case "truncated":
		return errorStyle
	default:
		return staleStyle
	}
}

// stalenessStyleFor returns a staleness-gradient style based on last message age.
// mirrors stop's approach: green (<1m) → yellow (<5m) → orange (<15m) → dark orange (<1h) → red (1h+).
func stalenessStyleFor(lastMessageTimeMS int64) lipgloss.Style {
	if lastMessageTimeMS <= 0 {
		return staleStyle
	}
	age := time.Duration(time.Now().UnixMilli()-lastMessageTimeMS) * time.Millisecond
	if age < time.Minute {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	}
	if age < 5*time.Minute {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	}
	if age < 15*time.Minute {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	}
	if age < time.Hour {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("202"))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
}

// titleWidth computes the flexible TITLE/LAST column width.
func (m model) titleWidth() int {
	fixed := colGap + colStatus + colGap + colSID + colGap + colUp +
		colGap + colCPU + colGap + colCtx + colGap + colModel
	return max(10, m.width-fixed-colGap)
}

// -- list view rendering --

func (m model) renderListView() string {
	if !m.ready {
		return "\n  loading...\n"
	}

	var b strings.Builder

	if display.showHeader {
		b.WriteString(m.renderHeader())
		b.WriteString("\n")
	}
	if display.showAggregateStats {
		b.WriteString(m.renderStatsBar())
		b.WriteString("\n")
	}
	if display.showColumnHeaders {
		if display.oneLine {
			b.WriteString(m.renderOneLineHeaders())
		} else {
			b.WriteString(m.renderColumnHeaders())
		}
		b.WriteString(dimStyle.Render(strings.Repeat("\u2500", m.width)))
		b.WriteString("\n")
	}

	visible := m.getVisibleSessions()

	overhead := m.listOverhead()
	linesPerSession := 3
	if display.oneLine {
		linesPerSession = 1
	}
	pageSize := max(1, (m.height-overhead)/linesPerSession)

	end := min(m.scrollOffset+pageSize, len(visible))
	for i := m.scrollOffset; i < end; i++ {
		isSelected := m.selectMode && i == m.cursor
		cs := visible[i]
		if display.oneLine {
			b.WriteString(m.renderSessionOneLine(cs, isSelected))
			b.WriteString("\n")
		} else {
			b.WriteString(m.renderSessionRow1(cs, isSelected))
			b.WriteString("\n")
			b.WriteString(m.renderSessionRow2(cs, isSelected))
			b.WriteString("\n\n")
		}
	}

	if m.selectMode {
		b.WriteString(m.renderDetailLine())
		b.WriteString("\n")
	}

	if m.showTodos {
		b.WriteString(m.renderTodosPanel())
	}
	if m.showMCPs {
		b.WriteString(m.renderMCPsPanel())
	}

	b.WriteString(m.renderFooter())

	return b.String()
}

// -- header --

func (m model) renderHeader() string {
	crumb := " opencode > sessions"
	if m.filterText != "" {
		crumb += " > /" + m.filterText
	}
	right := time.Now().Format("15:04:05") + " "
	pad := max(0, m.width-len(crumb)-len(right))
	line := crumb + strings.Repeat(" ", pad) + right
	if len(line) > m.width && m.width > 0 {
		line = line[:m.width]
	}
	return headerStyle.Render(line)
}

// -- stats bar --

func (m model) renderStatsBar() string {
	activeCount := 0
	toolCount := 0
	for _, cs := range m.sessions {
		if cs.session != nil && !cs.process.isToolProcess {
			activeCount++
		}
		if cs.process.isToolProcess {
			toolCount++
		}
	}

	running := fmt.Sprintf("%d active", activeCount)
	if toolCount > 0 {
		running += fmt.Sprintf(" (+%d bg)", toolCount)
	}

	sortLabel := columns[m.sortColIdx].label
	sortDir := "asc"
	if m.sortReverse {
		sortDir = "desc"
	}

	stats := fmt.Sprintf(" %s  %d/%d sessions  %d msgs  ctx:%s out:%s  sort:%s %s",
		running,
		m.todayStats.sessionCount, m.globalStats.sessionCount,
		m.todayStats.messageCount,
		formatTokens(m.todayStats.totalInput),
		formatTokens(m.todayStats.totalOutput),
		sortLabel, sortDir,
	)
	if len(stats) > m.width && m.width > 0 {
		stats = stats[:m.width]
	}
	return dimStyle.Render(stats)
}

// -- column headers (two rows) --

func (m model) renderColumnHeaders() string {
	tw := m.titleWidth()
	activeKey := columns[m.sortColIdx].key

	// header-to-sort-key mapping
	row1Cols := []struct {
		label, key string
		width      int
	}{
		{"TITLE", "title", tw},
		{"STATUS", "status", colStatus},
		{"SID", "sid", colSID},
		{"UP", "uptime", colUp},
		{"CPU", "cpu", colCPU},
		{"CTX", "tokens", colCtx},
		{"MODEL", "model", colModel},
	}
	row2Cols := []struct {
		label, key string
		width      int
	}{
		{"LAST", "last", tw},
		{"MSGS", "msgs", colStatus},
		{"PID", "pid", colSID},
		{"ROUND", "round", colUp},
		{"MEM", "mem", colCPU},
		{"OUT", "tokens", colCtx},
		{"TTY", "tty", colModel},
	}

	renderHdrRow := func(cols []struct {
		label, key string
		width      int
	}) string {
		var parts []string
		for _, c := range cols {
			text := truncOrPad(c.label, c.width)
			if c.key == activeKey {
				parts = append(parts, sortHiStyle.Render(text))
			} else {
				parts = append(parts, hdrDimBold.Render(text))
			}
		}
		return "  " + strings.Join(parts, "  ") + "\n"
	}

	return renderHdrRow(row1Cols) + renderHdrRow(row2Cols)
}

// -- session rows --

func (m model) renderSessionRow1(cs correlatedSession, selected bool) string {
	tw := m.titleWidth()
	nowMS := time.Now().UnixMilli()

	if cs.session == nil {
		text := "  " + truncOrPad(cs.process.cmdline, tw) +
			"  " + truncOrPad("no-session", colStatus) +
			"  " + truncOrPad("", colSID) +
			"  " + truncOrPad("", colUp) +
			"  " + truncOrPad("", colCPU) +
			"  " + truncOrPad("", colCtx) +
			"  " + truncOrPad("", colModel)
		if selected {
			return selectStyle.Width(m.width).MaxWidth(m.width).Render(text)
		}
		return dimStyle.Width(m.width).MaxWidth(m.width).Render(text)
	}

	status := inferStatus(cs.session, cs.process.cpuPercent)
	uptimeMS := int64(0)
	if cs.process.startTimeMS > 0 {
		uptimeMS = nowMS - cs.process.startTimeMS
	}

	text := "  " + truncOrPad(cs.session.title, tw) +
		"  " + truncOrPad(status, colStatus) +
		"  " + truncOrPad(cs.session.sessionID, colSID) +
		"  " + truncOrPad(formatDuration(uptimeMS), colUp) +
		"  " + truncOrPad(fmt.Sprintf("%.1f%%", cs.process.cpuPercent), colCPU) +
		"  " + truncOrPad(formatTokens(cs.session.totalInputTokens), colCtx) +
		"  " + truncOrPad(shortModel(cs.session.model), colModel)

	if selected {
		return selectStyle.Width(m.width).MaxWidth(m.width).Render(text)
	}
	var style lipgloss.Style
	if m.opinionatedColor {
		style = stalenessStyleFor(cs.session.lastMessageTime)
	} else {
		style = statusStyleFor(status)
	}
	return style.Width(m.width).MaxWidth(m.width).Render(text)
}

func (m model) renderSessionRow2(cs correlatedSession, selected bool) string {
	tw := m.titleWidth()
	nowMS := time.Now().UnixMilli()

	if cs.session == nil {
		text := "  " + truncOrPad(shortPath(cs.process.cwd, tw), tw) +
			"  " + truncOrPad("", colStatus) +
			"  " + truncOrPad(fmt.Sprintf("%d", cs.process.pid), colSID) +
			"  " + truncOrPad("", colUp) +
			"  " + truncOrPad("", colCPU) +
			"  " + truncOrPad("", colCtx) +
			"  " + truncOrPad(cs.process.tty, colModel)
		if selected {
			return selectStyle.Width(m.width).MaxWidth(m.width).Render(text)
		}
		return dimStyle.Width(m.width).MaxWidth(m.width).Render(text)
	}

	roundMS := int64(0)
	if cs.session.roundStartTime > 0 {
		roundMS = nowMS - cs.session.roundStartTime
	}

	text := "  " + truncOrPad(cs.session.lastOutput, tw) +
		"  " + truncOrPad(fmt.Sprintf("%d", cs.session.messageCount), colStatus) +
		"  " + truncOrPad(fmt.Sprintf("%d", cs.process.pid), colSID) +
		"  " + truncOrPad(formatDuration(roundMS), colUp) +
		"  " + truncOrPad(fmt.Sprintf("%.0fM", cs.process.memMB), colCPU) +
		"  " + truncOrPad(formatTokens(cs.session.totalOutputTokens), colCtx) +
		"  " + truncOrPad(cs.process.tty, colModel)

	if selected {
		return selectStyle.Width(m.width).MaxWidth(m.width).Render(text)
	}
	return dimStyle.Width(m.width).MaxWidth(m.width).Render(text)
}

// -- one-line mode rendering --

// listOverhead returns the number of non-session lines in the list view.
func (m model) listOverhead() int {
	lines := 1 // footer
	if display.showHeader {
		lines++
	}
	if display.showAggregateStats {
		lines++
	}
	if display.showColumnHeaders {
		if display.oneLine {
			lines += 2 // header row + separator
		} else {
			lines += 3 // two header rows + separator
		}
	}
	if m.selectMode {
		lines++ // detail line
	}
	if m.showTodos || m.showMCPs {
		lines += 8
	}
	return lines
}

// oneLineFlexWidth computes the width for flexible columns (width=0).
// splits remaining space evenly among all flexible columns.
func (m model) oneLineFlexWidth(cols []oneLineColSpec) int {
	fixed := 2 // leading indent
	flexCount := 0
	for i, c := range cols {
		if c.width > 0 {
			fixed += c.width
		} else {
			flexCount++
		}
		if i > 0 {
			fixed += colGap
		}
	}
	if flexCount == 0 {
		return 10
	}
	return max(5, (m.width-fixed)/flexCount)
}

func (m model) renderOneLineHeaders() string {
	cols := enabledOneLineColumns()
	if len(cols) == 0 {
		return ""
	}
	activeKey := columns[m.sortColIdx].key
	flexWidth := m.oneLineFlexWidth(cols)

	var parts []string
	for _, c := range cols {
		w := c.width
		if w == 0 {
			w = flexWidth
		}
		text := truncOrPad(c.label, w)
		if c.key == activeKey {
			parts = append(parts, sortHiStyle.Render(text))
		} else {
			parts = append(parts, hdrDimBold.Render(text))
		}
	}
	return "  " + strings.Join(parts, "  ") + "\n"
}

func (m model) renderSessionOneLine(cs correlatedSession, selected bool) string {
	cols := enabledOneLineColumns()
	if len(cols) == 0 {
		return ""
	}
	flexWidth := m.oneLineFlexWidth(cols)

	var parts []string
	for _, c := range cols {
		w := c.width
		if w == 0 {
			w = flexWidth
		}
		val := columnValue(c.key, cs)
		if c.key == "last" && display.ticker.rateMS > 0 {
			parts = append(parts, tickerSlice(val, w, display.ticker.rateMS))
		} else {
			parts = append(parts, truncOrPad(val, w))
		}
	}

	text := "  " + strings.Join(parts, "  ")

	if selected {
		return selectStyle.Width(m.width).MaxWidth(m.width).Render(text)
	}
	if cs.session == nil {
		return dimStyle.Width(m.width).MaxWidth(m.width).Render(text)
	}
	var style lipgloss.Style
	if m.opinionatedColor {
		style = stalenessStyleFor(cs.session.lastMessageTime)
	} else {
		style = statusStyleFor(inferStatus(cs.session, cs.process.cpuPercent))
	}
	return style.Width(m.width).MaxWidth(m.width).Render(text)
}

// -- detail line (cwd of selected) --

func (m model) renderDetailLine() string {
	visible := m.getVisibleSessions()
	if m.cursor >= len(visible) {
		return ""
	}
	cs := visible[m.cursor]
	cwdDisplay := shortPath(cs.process.cwd, max(10, m.width-4))
	return dimStyle.Render(" " + cwdDisplay)
}

// -- panels --

func (m model) renderTodosPanel() string {
	var b strings.Builder
	b.WriteString(dimStyle.Render(strings.Repeat("\u2500", m.width)))
	b.WriteString("\n")
	b.WriteString(panelStyle.Render(" TODOS (selected session)"))
	b.WriteString("\n")

	visible := m.getVisibleSessions()
	if m.cursor < len(visible) {
		if s := visible[m.cursor].session; s != nil && len(s.activeTodos) > 0 {
			limit := min(6, len(s.activeTodos))
			for _, todo := range s.activeTodos[:limit] {
				statusChar := map[string]string{
					"completed":   "x",
					"in_progress": ">",
					"pending":     " ",
					"cancelled":   "-",
				}[todo.status]
				if statusChar == "" {
					statusChar = "?"
				}
				priorityStyle, ok := map[string]lipgloss.Style{
					"high":   errorStyle,
					"medium": transStyle,
					"low":    dimStyle,
				}[todo.priority]
				if !ok {
					priorityStyle = idleStyle
				}
				line := fmt.Sprintf(" [%s] %s", statusChar, todo.content)
				if len(line) > m.width && m.width > 0 {
					line = line[:m.width]
				}
				b.WriteString(priorityStyle.Render(line))
				b.WriteString("\n")
			}
		} else {
			b.WriteString(dimStyle.Render("  (no todos)"))
			b.WriteString("\n")
		}
	}

	return b.String()
}

func (m model) renderMCPsPanel() string {
	var b strings.Builder
	b.WriteString(dimStyle.Render(strings.Repeat("\u2500", m.width)))
	b.WriteString("\n")
	b.WriteString(panelStyle.Render(" MCP SERVERS"))
	b.WriteString("\n")

	if len(m.mcpConfig) == 0 {
		b.WriteString(dimStyle.Render("  (no config found)"))
		b.WriteString("\n")
		return b.String()
	}

	var enabled, disabled []string
	for name, cfg := range m.mcpConfig {
		cfgMap, ok := cfg.(map[string]any)
		if !ok {
			disabled = append(disabled, name)
			continue
		}
		if en, ok := cfgMap["enabled"].(bool); ok && !en {
			disabled = append(disabled, name)
		} else {
			enabled = append(enabled, name)
		}
	}

	if len(enabled) > 0 {
		line := "  enabled: " + strings.Join(enabled, ", ")
		if len(line) > m.width && m.width > 0 {
			line = line[:m.width]
		}
		b.WriteString(activeStyle.Render(line))
		b.WriteString("\n")
	}
	if len(disabled) > 0 {
		names := strings.Join(disabled, ", ")
		if len(disabled) > 5 {
			names = strings.Join(disabled[:5], ", ") + "..."
		}
		line := fmt.Sprintf("  disabled: %d servers (%s)", len(disabled), names)
		if len(line) > m.width && m.width > 0 {
			line = line[:m.width]
		}
		b.WriteString(dimStyle.Render(line))
		b.WriteString("\n")
	}

	return b.String()
}

// -- footer --

func (m model) renderFooter() string {
	if m.filterActive {
		prompt := " /" + m.filterText
		return headerStyle.Width(m.width).Render(prompt)
	}

	binds := []struct{ key, desc string }{
		{"q", "quit"},
		{"enter", "view"},
		{"r", "refresh"},
		{"y", "yank"},
		{">/<", "sort"},
		{"s", "flip"},
		{"/", "filter"},
		{"esc", "deselect"},
		{"a", "sessions"},
		{"p", "procs"},
		{"t", "todos"},
		{"m", "mcps"},
		{"c", "colors"},
		{"j/k", "select"},
	}

	var parts []string
	for _, b := range binds {
		parts = append(parts, keyStyle.Render(b.key)+" "+helpStyle.Render(b.desc))
	}
	bar := " " + strings.Join(parts, "  ")

	// flash message overlay
	if m.flashMsg != "" && time.Since(m.flashTime) < 1500*time.Millisecond {
		flash := " " + m.flashMsg + " "
		flashRendered := activeStyle.Bold(true).Render(flash)
		barWidth := lipgloss.Width(bar)
		flashWidth := lipgloss.Width(flashRendered)
		if barWidth+flashWidth < m.width {
			pad := m.width - barWidth - flashWidth
			return bar + strings.Repeat(" ", pad) + flashRendered
		}
	}

	// subtle mode indicator, right-aligned
	if m.selectMode {
		indicator := dimStyle.Render("select")
		barWidth := lipgloss.Width(bar)
		indWidth := lipgloss.Width(indicator)
		if barWidth+indWidth+2 < m.width {
			pad := m.width - barWidth - indWidth
			return bar + strings.Repeat(" ", pad) + indicator
		}
	}

	return bar
}
