// SwiftBar plugin output for macOS menu bar session status.
//
// outputs one-shot SwiftBar-formatted text to stdout. intended to be
// called by a SwiftBar plugin script on a periodic cadence.
//
// setup:
//   1. install SwiftBar (brew install swiftbar)
//   2. create a plugin script in SwiftBar's plugin folder, e.g. otop-bar.3s.sh:
//        #!/bin/bash
//        /path/to/otop bar-status -p 8390
//   3. chmod +x the script; SwiftBar picks it up automatically
//
// the filename suffix determines refresh cadence (3s in the example).
// requires otop serve to be running on the specified port.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ANSI color codes for the menu bar title line.
// each status group gets its own color so you can distinguish
// them at a glance without reading the letters.
const (
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiMagenta = "\033[35m"
	ansiRed     = "\033[31m"
	ansiDim     = "\033[90m"
	ansiReset   = "\033[0m"
)

// barGroup defines a status category for grouping sessions.
// ordered by urgency: asking and errors surface first in the
// title so they catch your eye immediately.
type barGroup struct {
	key      string   // internal identifier
	label    string   // dropdown section header
	letter   string   // single-char abbreviation for title
	ansi     string   // ANSI escape for title color (empty = default text)
	hex      string   // hex color for dropdown items
	statuses []string // which inferStatus values belong here
}

// barGroups defines status categories in urgency order.
// zero-count groups are omitted from the title, so during normal
// operation you just see "G5 I3" (green active, plain idle).
// when something needs attention, "A2" appears first in magenta.
var barGroups = []barGroup{
	{"asking", "Asking", "A", ansiMagenta, "#ff9500", []string{"asking"}},
	{"active", "Active", "G", ansiGreen, "#4ec34e", []string{"generating", "tool use", "busy"}},
	{"thinking", "Thinking", "T", ansiYellow, "#d4a72c", []string{"thinking", "queued"}},
	{"error", "Error", "X", ansiRed, "#ff3b30", []string{"truncated"}},
	{"idle", "Idle", "I", "", "#999999", []string{"idle"}},
	{"stale", "Stale", "S", ansiDim, "#666666", []string{"stale", "unknown"}},
}

// barSessionData is the subset of session fields decoded from the serve endpoint.
type barSessionData struct {
	Title      string `json:"title"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	RoundMS    int64  `json:"round_ms"`
	LastOutput string `json:"last_output"`
}

// barAPIResponse matches the /sessions endpoint JSON structure.
type barAPIResponse struct {
	Sessions []barSessionData `json:"sessions"`
	Today    struct {
		SessionCount int   `json:"session_count"`
		MessageCount int   `json:"message_count"`
		TotalInput   int64 `json:"total_input"`
		TotalOutput  int64 `json:"total_output"`
	} `json:"today"`
}

// barStatusCommand fetches session data from otop serve and
// outputs SwiftBar-formatted text to stdout.
func barStatusCommand(port int) {
	serveURL := fmt.Sprintf("http://localhost:%d/sessions", port)

	data, err := barFetch(serveURL)
	if err != nil {
		barPrintError(err)
		return
	}

	grouped := barGroupSessions(data.Sessions)
	barPrintTitle(grouped, len(data.Sessions))
	fmt.Println("---")

	if len(data.Sessions) == 0 {
		fmt.Println("no sessions running | color=#666666")
	} else {
		barPrintSections(grouped)
	}

	barPrintStats(data)
	barPrintActions()
}

// barFetch GETs the session data from the serve endpoint with a short timeout.
func barFetch(url string) (*barAPIResponse, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var data barAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

// barGroupSessions buckets sessions by their status group key.
func barGroupSessions(sessions []barSessionData) map[string][]barSessionData {
	grouped := make(map[string][]barSessionData)
	for _, s := range sessions {
		key := barGroupKeyFor(s.Status)
		grouped[key] = append(grouped[key], s)
	}
	return grouped
}

// barGroupKeyFor returns the group key for a given status string.
func barGroupKeyFor(status string) string {
	for _, g := range barGroups {
		for _, s := range g.statuses {
			if s == status {
				return g.key
			}
		}
	}
	return "stale"
}

// barPriorityColor returns the hex color of the highest-urgency non-zero group.
// used for the SF Symbol icon color so the icon itself signals overall health.
func barPriorityColor(grouped map[string][]barSessionData) string {
	for _, g := range barGroups {
		if len(grouped[g.key]) > 0 {
			return g.hex
		}
	}
	return "#666666"
}

// barPrintTitle outputs the ANSI-colored menu bar title.
// format: "G3 T1 A2 I5" — letter + count per non-zero group, colored by ANSI.
// optionally includes an SF Symbol icon (controlled by display.bar.showIcon).
func barPrintTitle(grouped map[string][]barSessionData, totalSessions int) {
	iconSuffix := ""
	if display.bar.showIcon {
		sfcolor := barPriorityColor(grouped)
		iconSuffix = fmt.Sprintf(" sfimage=%s sfcolor=%s", display.bar.icon, sfcolor)
	}

	if totalSessions == 0 {
		if display.bar.showIcon {
			fmt.Printf("| sfimage=%s sfcolor=#666666\n", display.bar.icon)
		} else {
			fmt.Println("- | color=#666666")
		}
		return
	}

	var parts []string
	for _, g := range barGroups {
		count := len(grouped[g.key])
		if count == 0 {
			continue
		}
		if g.ansi != "" {
			parts = append(parts, fmt.Sprintf("%s%s%d%s", g.ansi, g.letter, count, ansiReset))
		} else {
			parts = append(parts, fmt.Sprintf("%s%d", g.letter, count))
		}
	}

	fmt.Printf("%s | ansi=true%s\n", strings.Join(parts, " "), iconSuffix)
}

// barPrintSections outputs dropdown sections grouped by status.
// each section has a colored header and indented session entries
// showing title, model, and round duration.
func barPrintSections(grouped map[string][]barSessionData) {
	for _, g := range barGroups {
		sessions := grouped[g.key]
		if len(sessions) == 0 {
			continue
		}

		fmt.Printf("%s (%d) | color=%s size=13\n", g.label, len(sessions), g.hex)
		for _, s := range sessions {
			title := s.Title
			if len(title) > 35 {
				title = title[:35] + "..."
			}
			round := formatDuration(s.RoundMS)
			fmt.Printf("  %s · %s · %s | color=%s size=12\n", title, s.Model, round, g.hex)
		}
	}
}

// barPrintStats outputs today's aggregate session stats.
func barPrintStats(data *barAPIResponse) {
	fmt.Println("---")
	fmt.Printf("today: %d sessions · %d msgs · %s ctx | color=#666666 size=11\n",
		data.Today.SessionCount,
		data.Today.MessageCount,
		formatTokens(data.Today.TotalInput),
	)
}

// barPrintActions outputs the action items at the bottom of the dropdown.
func barPrintActions() {
	fmt.Println("---")
	fmt.Println("Refresh | refresh=true")
}

// barPrintError outputs an error state for the menu bar.
// shows a warning icon and the error message in the dropdown.
func barPrintError(err error) {
	if display.bar.showIcon {
		fmt.Println("| sfimage=exclamationmark.triangle.fill sfcolor=#ff3b30")
	} else {
		fmt.Println("err | color=#ff3b30 ansi=false")
	}
	fmt.Println("---")
	fmt.Println("otop serve unreachable | color=#ff3b30")
	// sanitize error string: pipes break SwiftBar formatting, newlines break lines
	errStr := strings.NewReplacer("|", "/", "\n", " ").Replace(err.Error())
	if len(errStr) > 60 {
		errStr = errStr[:60] + "..."
	}
	fmt.Printf("%s | color=#666666 size=11\n", errStr)
	fmt.Println("---")
	fmt.Println("Refresh | refresh=true")
}
