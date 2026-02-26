// detail view: tmux pane capture and db message display.
//
// pressing enter on a session opens a full-screen detail view.
// primary: captures the live terminal via tmux. fallback: db messages.

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// -- tmux integration --

// tmuxPaneForTTY maps a TTY name (e.g. "ttys005") to a tmux pane target.
func tmuxPaneForTTY(tty string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-a", "-F",
		"#{pane_tty} #{session_name}:#{window_index}.#{pane_index}").Output()
	if err != nil {
		return ""
	}

	devTTY := "/dev/" + tty
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && parts[0] == devTTY {
			return parts[1]
		}
	}
	return ""
}

// captureTmuxPane captures the screen content of a tmux pane by TTY.
// returns nil if tmux isn't available or the TTY isn't in a pane.
func captureTmuxPane(tty string) []string {
	target := tmuxPaneForTTY(tty)
	if target == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", target, "-p").Output()
	if err != nil {
		return nil
	}
	return strings.Split(string(out), "\n")
}

// formatDBMessages formats message details into displayable lines.
func formatDBMessages(msgs []messageDetail) []string {
	if len(msgs) == 0 {
		return []string{"  (no messages)"}
	}

	var lines []string
	for _, msg := range msgs {
		ts := ""
		if msg.timeCreated > 0 {
			ts = time.Unix(msg.timeCreated/1000, 0).Format("15:04:05")
		}

		tokens := ""
		if msg.tokensOut > 0 {
			tokens = fmt.Sprintf(" ctx:%s out:%s",
				formatTokens(msg.tokensIn+msg.cacheRead),
				formatTokens(msg.tokensOut))
		}

		header := fmt.Sprintf(" %s  %-10s %-12s%s", ts, msg.role, msg.finish, tokens)
		lines = append(lines, header)

		if msg.textPreview != "" {
			preview := strings.ReplaceAll(msg.textPreview, "\n", " ")
			for len(preview) > 0 {
				chunk := preview
				if len(chunk) > 76 {
					chunk = preview[:76]
					preview = preview[76:]
				} else {
					preview = ""
				}
				lines = append(lines, "            "+chunk)
			}
		}
		lines = append(lines, "") // blank separator
	}

	return lines
}

// -- detail view rendering --

func (m model) renderDetailView() string {
	var b strings.Builder

	proc := m.detailSession.process
	session := m.detailSession.session

	// header breadcrumb
	title := "(no session)"
	sid := "-"
	status := "?"
	if session != nil {
		title = session.title
		sid = session.sessionID
		status = inferStatus(session, proc.cpuPercent)
	}
	sourceTag := ""
	if m.detailSource != "" {
		sourceTag = "[" + m.detailSource + "]"
	}

	crumb := fmt.Sprintf(" opencode > sessions > %s %s", sid, sourceTag)
	right := status + " "
	pad := max(0, m.width-len(crumb)-len(right))
	headerLine := crumb + strings.Repeat(" ", pad) + right
	if len(headerLine) > m.width && m.width > 0 {
		headerLine = headerLine[:m.width]
	}
	b.WriteString(headerStyle.Render(headerLine))
	b.WriteString("\n")

	// info bar
	var infoParts []string
	if session != nil {
		t := title
		if len(t) > 40 {
			t = t[:40]
		}
		infoParts = append(infoParts, t)
		infoParts = append(infoParts, fmt.Sprintf("pid:%d", proc.pid))
		infoParts = append(infoParts, fmt.Sprintf("tty:%s", proc.tty))
		infoParts = append(infoParts, shortPath(proc.cwd, 30))
	}
	infoLine := " " + strings.Join(infoParts, "  ")
	if len(infoLine) > m.width && m.width > 0 {
		infoLine = infoLine[:m.width]
	}
	b.WriteString(dimStyle.Render(infoLine))
	b.WriteString("\n")

	// separator
	b.WriteString(dimStyle.Render(strings.Repeat("\u2500", m.width)))
	b.WriteString("\n")

	// scrollable content
	contentRows := max(1, m.height-4) // header + info + sep + footer
	end := min(m.detailScroll+contentRows, len(m.detailLines))
	for i := m.detailScroll; i < end; i++ {
		line := m.detailLines[i]
		if len(line) > m.width && m.width > 0 {
			line = line[:m.width]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	// footer
	footer := " " +
		keyStyle.Render("esc") + " " + helpStyle.Render("back") + "  " +
		keyStyle.Render("r") + " " + helpStyle.Render("refresh") + "  " +
		keyStyle.Render("j/k") + " " + helpStyle.Render("scroll") + "  " +
		keyStyle.Render("tab") + " " + helpStyle.Render("toggle tmux/db")
	b.WriteString(footer)

	return b.String()
}
