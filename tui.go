// bubbletea model, update loop, and commands.
//
// follows the elm architecture: model holds all state, Update is a pure
// state transition, View renders to string. side effects happen in
// tea.Cmd functions (fetchCmd, tickCmd, etc.)

package main

import (
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// -- messages --

type dataMsg fetchResult
type tickMsg time.Time

type detailRefreshMsg struct {
	lines  []string
	source string
}

type detailToggleMsg struct {
	lines  []string
	source string
}

type tickerTickMsg struct{}

// -- model --

type model struct {
	// terminal dimensions
	width  int
	height int

	// data from last fetch
	sessions    []correlatedSession
	todayStats  aggStats
	globalStats aggStats
	mcpConfig   map[string]any

	// list view state
	cursor           int
	scrollOffset     int
	sortColIdx       int
	sortReverse      bool
	filterText       string
	filterActive     bool
	showAllProcesses bool
	showAllSessions  bool
	showTodos        bool
	showMCPs         bool

	// detail view state
	detailMode    bool
	detailScroll  int
	detailLines   []string
	detailSession *correlatedSession
	detailSource  string // "tmux" or "db"

	// view vs select mode
	// view mode: no cursor highlight, just watching
	// select mode: cursor visible, nav/enter/yank work
	selectMode bool

	// flash message (e.g. after yank)
	flashMsg  string
	flashTime time.Time

	ready bool
}

func newModel() model {
	sortIdx := 0
	for i, col := range columns {
		if col.key == display.defaultSortKey {
			sortIdx = i
			break
		}
	}
	return model{
		sortColIdx:  sortIdx,
		sortReverse: display.defaultSortReverse,
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchCmd, tickCmd()}
	if display.oneLine && display.ticker.rateMS > 0 {
		cmds = append(cmds, tickerTickCmd())
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.detailMode {
			return m.handleDetailKey(msg)
		}
		if m.filterActive {
			return m.handleFilterKey(msg)
		}
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case dataMsg:
		return m.handleData(fetchResult(msg))
	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())
		if m.detailMode && m.detailSource == "tmux" {
			cmds = append(cmds, m.refreshDetailCmd())
		}
		if !m.detailMode {
			cmds = append(cmds, fetchCmd)
		}
		return m, tea.Batch(cmds...)
	case detailRefreshMsg:
		m.detailLines = msg.lines
		if msg.source != "" {
			m.detailSource = msg.source
		}
		return m, nil
	case detailToggleMsg:
		if len(msg.lines) > 0 {
			m.detailLines = msg.lines
			m.detailSource = msg.source
			m.detailScroll = 0
		}
		return m, nil
	case tickerTickMsg:
		return m, tickerTickCmd()
	}
	return m, nil
}

func (m model) View() string {
	if m.detailMode {
		return m.renderDetailView()
	}
	return m.renderListView()
}

// -- key handlers --

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		return m, fetchCmd
	case "t":
		m.showTodos = !m.showTodos
	case "m":
		m.showMCPs = !m.showMCPs
	case "a":
		m.showAllSessions = !m.showAllSessions
	case "p":
		m.showAllProcesses = !m.showAllProcesses
	case "y":
		m.selectMode = true
		visible := m.getVisibleSessions()
		if m.cursor < len(visible) {
			if s := visible[m.cursor].session; s != nil {
				_ = exec.Command("pbcopy").Run()
				cmd := exec.Command("pbcopy")
				cmd.Stdin = strings.NewReader(s.sessionID)
				_ = cmd.Run()
				m.flashMsg = "yanked: " + s.sessionID
				m.flashTime = time.Now()
			}
		}
	case "enter":
		m.selectMode = true
		visible := m.getVisibleSessions()
		if m.cursor < len(visible) {
			cs := visible[m.cursor]
			m.detailSession = &cs
			m.detailScroll = 0
			m.detailMode = true
			return m, m.refreshDetailCmd()
		}
	case ">", ".":
		m.sortColIdx = (m.sortColIdx + 1) % len(columns)
	case "<", ",":
		m.sortColIdx = (m.sortColIdx - 1 + len(columns)) % len(columns)
	case "s":
		m.sortReverse = !m.sortReverse

	case "/":
		m.filterActive = true
		m.filterText = ""
	case "esc":
		if m.filterText != "" {
			m.filterText = ""
		} else {
			m.selectMode = false
		}
	case "j", "down":
		m.selectMode = true
		visible := m.getVisibleSessions()
		maxIdx := max(0, len(visible)-1)
		m.cursor = min(m.cursor+1, maxIdx)
	case "k", "up":
		m.selectMode = true
		m.cursor = max(m.cursor-1, 0)
	}

	// clamp cursor after filter/toggle changes
	visible := m.getVisibleSessions()
	maxIdx := max(0, len(visible)-1)
	m.cursor = min(m.cursor, maxIdx)
	m.adjustScroll()

	return m, nil
}

func (m model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterText = ""
		m.filterActive = false
	case "enter":
		m.filterActive = false
	case "backspace":
		if len(m.filterText) > 0 {
			m.filterText = m.filterText[:len(m.filterText)-1]
		}
	default:
		// only append printable single characters
		if len(msg.String()) == 1 {
			ch := msg.String()[0]
			if ch >= 32 && ch < 127 {
				m.filterText += string(ch)
			}
		}
	}
	return m, nil
}

func (m model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.detailMode = false
		return m, fetchCmd
	case "r":
		return m, m.refreshDetailCmd()
	case "tab":
		return m, m.toggleDetailSourceCmd()
	case "j", "down":
		maxScroll := max(0, len(m.detailLines)-10)
		m.detailScroll = min(m.detailScroll+1, maxScroll)
	case "k", "up":
		m.detailScroll = max(m.detailScroll-1, 0)
	case "d", "pgdown":
		maxScroll := max(0, len(m.detailLines)-10)
		m.detailScroll = min(m.detailScroll+m.height/2, maxScroll)
	case "u", "pgup":
		m.detailScroll = max(m.detailScroll-m.height/2, 0)
	}
	return m, nil
}

// -- data handling --

func (m model) handleData(result fetchResult) (tea.Model, tea.Cmd) {
	m.sessions = result.correlated
	m.todayStats = result.todayStats
	m.globalStats = result.globalStats
	m.mcpConfig = result.mcpConfig
	m.ready = true

	// clamp cursor after data change
	visible := m.getVisibleSessions()
	maxIdx := max(0, len(visible)-1)
	m.cursor = min(m.cursor, maxIdx)
	m.adjustScroll()

	return m, nil
}

// -- filtering + sorting --

func (m model) getVisibleSessions() []correlatedSession {
	var filtered []correlatedSession
	for _, cs := range m.sessions {
		if !m.showAllProcesses && (cs.process.isToolProcess || cs.session == nil) {
			continue
		}
		if !m.showAllSessions && cs.session != nil && !cs.session.interactive {
			continue
		}
		if m.filterText != "" {
			needle := strings.ToLower(m.filterText)
			matches := false
			if cs.session != nil {
				matches = strings.Contains(strings.ToLower(cs.session.title), needle) ||
					strings.Contains(strings.ToLower(cs.session.model), needle) ||
					strings.Contains(strings.ToLower(cs.session.sessionID), needle) ||
					strings.Contains(strings.ToLower(inferStatus(cs.session, cs.process.cpuPercent)), needle)
			}
			matches = matches ||
				strings.Contains(strings.ToLower(cs.process.cwd), needle) ||
				strings.Contains(strings.ToLower(cs.process.tty), needle)
			if !matches {
				continue
			}
		}
		filtered = append(filtered, cs)
	}

	key := columns[m.sortColIdx].key
	sort.SliceStable(filtered, func(i, j int) bool {
		cmp := compareSessions(key, filtered[i], filtered[j])
		if m.sortReverse {
			return cmp > 0
		}
		return cmp < 0
	})

	return filtered
}

func (m *model) adjustScroll() {
	overhead := m.listOverhead()
	linesPerSession := 3
	if display.oneLine {
		linesPerSession = 1
	}
	pageSize := max(1, (m.height-overhead)/linesPerSession)
	if m.cursor >= m.scrollOffset+pageSize {
		m.scrollOffset = m.cursor - pageSize + 1
	}
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
}

// -- commands --

func fetchCmd() tea.Msg {
	return dataMsg(fetchAll())
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func tickerTickCmd() tea.Cmd {
	return tea.Tick(time.Duration(display.ticker.rateMS)*time.Millisecond, func(t time.Time) tea.Msg {
		return tickerTickMsg{}
	})
}

func (m model) refreshDetailCmd() tea.Cmd {
	proc := m.detailSession.process
	session := m.detailSession.session
	return func() tea.Msg {
		lines := captureTmuxPane(proc.tty)
		if lines != nil {
			return detailRefreshMsg{lines: lines, source: "tmux"}
		}
		if session != nil {
			return detailRefreshMsg{
				lines:  formatDBMessages(getRecentMessages(session.sessionID, 30)),
				source: "db",
			}
		}
		return detailRefreshMsg{lines: []string{"  (no data)"}}
	}
}

func (m model) toggleDetailSourceCmd() tea.Cmd {
	currentSource := m.detailSource
	proc := m.detailSession.process
	session := m.detailSession.session
	return func() tea.Msg {
		if currentSource == "tmux" {
			if session != nil {
				return detailToggleMsg{
					lines:  formatDBMessages(getRecentMessages(session.sessionID, 30)),
					source: "db",
				}
			}
			return detailToggleMsg{lines: []string{"  (no session data)"}, source: "db"}
		}
		// try tmux
		lines := captureTmuxPane(proc.tty)
		if lines != nil {
			return detailToggleMsg{lines: lines, source: "tmux"}
		}
		return detailToggleMsg{} // stay on current
	}
}
