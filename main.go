package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// `otop sessions` subcommand — JSON output for scripting
	if len(os.Args) > 1 && os.Args[1] == "sessions" {
		fs := flag.NewFlagSet("sessions", flag.ExitOnError)
		all := fs.Bool("all", false, "include tool processes and unmatched")
		fs.BoolVar(all, "a", false, "include tool processes and unmatched")
		noninteractive := fs.Bool("include-noninteractive", false, "include non-interactive sessions")
		_ = fs.Parse(os.Args[2:])

		if _, err := os.Stat(dbPath()); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error: db not found at %s\n", dbPath())
			os.Exit(1)
		}
		sessionsCommand(*all, *noninteractive)
		return
	}

	// default: launch TUI
	if _, err := os.Stat(dbPath()); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: opencode db not found at %s\n", dbPath())
		os.Exit(1)
	}

	// clean exit on SIGTERM/SIGHUP so alt screen gets restored
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigCh
		os.Exit(0)
	}()

	setProcessTitle()

	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// setProcessTitle sets tmux window name and xterm title.
func setProcessTitle() {
	fmt.Print("\033kotop\033\\")
	fmt.Print("\033]2;otop\007")
}

// sessionsCommand outputs running opencode sessions as JSON.
func sessionsCommand(includeAll, includeNoninteractive bool) {
	_, correlated := correlateAllSessions()

	var results []map[string]any
	for _, cs := range correlated {
		if !includeAll && (cs.process.isToolProcess || cs.session == nil) {
			continue
		}
		if !includeNoninteractive && cs.session != nil && !cs.session.interactive {
			continue
		}

		tmuxPane := tmuxPaneForTTY(cs.process.tty)

		entry := map[string]any{
			"pid":             cs.process.pid,
			"tty":             cs.process.tty,
			"cwd":             cs.process.cwd,
			"cpu_percent":     cs.process.cpuPercent,
			"mem_mb":          cs.process.memMB,
			"is_tool_process": cs.process.isToolProcess,
			"tmux_pane":       tmuxPane,
		}

		if cs.session != nil {
			entry["session"] = map[string]any{
				"id":            cs.session.sessionID,
				"title":         cs.session.title,
				"directory":     cs.session.directory,
				"model":         cs.session.model,
				"status":        inferStatus(cs.session, cs.process.cpuPercent),
				"message_count": cs.session.messageCount,
				"interactive":   cs.session.interactive,
			}
		}

		results = append(results, entry)
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(out))
}
