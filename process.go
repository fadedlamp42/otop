// process discovery: ps + lsof queries for finding opencode instances.
//
// finds running opencode processes via `ps`, then uses a single batched
// `lsof` call to extract each process's cwd and open log file path.
// the log filename encodes the process start time in UTC, which is used
// for tier 2 PID-to-session correlation.

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sessionIDRe = regexp.MustCompile(`(?:^|\s)-s\s+(ses_\S+)`)

type tmuxPaneInfo struct {
	session string
	window  string
}

// batchTmuxSessions maps TTY names (e.g. "ttys005") to tmux session and
// window names via a single tmux list-panes call.
func batchTmuxSessions() map[string]tmuxPaneInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-a", "-F",
		"#{pane_tty} #{session_name} #{window_name}").Output()
	if err != nil {
		return nil
	}

	result := make(map[string]tmuxPaneInfo)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		tty := parts[0]
		if strings.HasPrefix(tty, "/dev/") {
			tty = tty[5:]
		}
		info := tmuxPaneInfo{session: parts[1]}
		if len(parts) == 3 {
			info.window = parts[2]
		}
		result[tty] = info
	}
	return result
}

// parseLogTimestamp extracts epoch ms from an opencode log filename.
// log filenames are UTC timestamps: 2026-02-20T145658.log
// IMPORTANT: must be parsed as UTC, not local time.
func parseLogTimestamp(logpath string) int64 {
	base := filepath.Base(logpath)
	re := regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})T(\d{2})(\d{2})(\d{2})\.log`)
	m := re.FindStringSubmatch(base)
	if m == nil {
		return 0
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	hour, _ := strconv.Atoi(m[4])
	min, _ := strconv.Atoi(m[5])
	sec, _ := strconv.Atoi(m[6])
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
	return t.UnixMilli()
}

// lsofInfo holds cwd and log path extracted from a single lsof call.
type lsofInfo struct {
	cwd     string
	logpath string
}

// batchLsof runs a single lsof call for all PIDs.
// extracts cwd and opencode log file paths. even unlinked log files
// are visible via lsof (unix keeps the inode alive while the fd is open).
func batchLsof(pids []int) map[int]lsofInfo {
	result := make(map[int]lsofInfo)
	if len(pids) == 0 {
		return result
	}
	for _, p := range pids {
		result[p] = lsofInfo{cwd: "?"}
	}

	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "lsof", "-p", strings.Join(pidStrs, ",")).Output()
	if err != nil {
		return result
	}

	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 9 {
			continue
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		info, ok := result[pid]
		if !ok {
			continue
		}
		fdCol := parts[3]
		path := parts[len(parts)-1]
		if fdCol == "cwd" {
			info.cwd = path
		}
		if strings.Contains(path, ".log") && strings.Contains(path, "opencode/log/") {
			info.logpath = path
		}
		result[pid] = info
	}

	return result
}

// getOpencodeProcesses finds all running opencode processes via ps + lsof.
// filters to processes whose binary basename is literally "opencode",
// excluding this tool and grep artifacts.
func getOpencodeProcesses() []processInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ps", "axo", "pid,pcpu,rss,tty,etime,args").Output()
	if err != nil {
		return nil
	}

	type rawProc struct {
		pid     int
		cpu     float64
		rss     int
		tty     string
		elapsed string
		args    string
	}

	var raw []rawProc
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines[1:] {
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		args := strings.Join(parts[5:], " ")
		if !strings.Contains(args, "opencode") {
			continue
		}
		if strings.Contains(args, "opencode-htop") || strings.Contains(args, "otop") || strings.Contains(args, "grep") {
			continue
		}
		argParts := strings.Fields(args)
		if len(argParts) == 0 || filepath.Base(argParts[0]) != "opencode" {
			continue
		}

		pid, _ := strconv.Atoi(parts[0])
		cpu, _ := strconv.ParseFloat(parts[1], 64)
		rss, _ := strconv.Atoi(parts[2])

		raw = append(raw, rawProc{
			pid:     pid,
			cpu:     cpu,
			rss:     rss,
			tty:     parts[3],
			elapsed: parts[4],
			args:    args,
		})
	}

	// single batched lsof for all PIDs
	pids := make([]int, len(raw))
	for i, r := range raw {
		pids[i] = r.pid
	}
	lsofResults := batchLsof(pids)

	var processes []processInfo
	for _, r := range raw {
		info := lsofResults[r.pid]

		// tier 1: parse -s flag from cmdline
		var sessionID string
		if m := sessionIDRe.FindStringSubmatch(r.args); m != nil {
			sessionID = m[1]
		}

		// tier 2: start time from log filename (UTC)
		var startMS int64
		if info.logpath != "" {
			startMS = parseLogTimestamp(info.logpath)
		}

		// detect tool processes (opencode run)
		argParts := strings.Fields(r.args)
		isTool := len(argParts) > 1 && argParts[1] == "run"

		processes = append(processes, processInfo{
			pid:           r.pid,
			cpuPercent:    r.cpu,
			memMB:         float64(r.rss) / 1024,
			elapsed:       r.elapsed,
			tty:           r.tty,
			cwd:           info.cwd,
			cmdline:       r.args,
			sessionID:     sessionID,
			startTimeMS:   startMS,
			isToolProcess: isTool,
		})
	}

	// batch tmux session lookup
	tmuxSessions := batchTmuxSessions()
	for i := range processes {
		if info, ok := tmuxSessions[processes[i].tty]; ok {
			processes[i].tmuxSession = info.session
			processes[i].tmuxWindow = info.window
		}
	}

	return processes
}
