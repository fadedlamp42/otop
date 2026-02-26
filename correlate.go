// PID-to-session correlation: the two-pass claimed-set algorithm.
//
// pass 1: explicit -s flags claim sessions immediately.
// pass 2: remaining processes sorted by start_time ascending, each
//         claims the best unclaimed session from its candidates.
//
// also contains fetchAll() which runs all data collection concurrently.

package main

import (
	"sort"
	"sync"
)

// correlateAllSessions runs the full two-pass PID-to-session algorithm.
// returns the raw process list and the correlated (process, session) pairs.
func correlateAllSessions() ([]processInfo, []correlatedSession) {
	processes := getOpencodeProcesses()

	claimed := make(map[string]bool)
	resolved := make(map[int]string) // pid → session_id

	// pass 1: explicit session IDs from cmdline -s flag
	for _, proc := range processes {
		if proc.sessionID != "" && !proc.isToolProcess {
			claimed[proc.sessionID] = true
			resolved[proc.pid] = proc.sessionID
		}
	}

	// pass 2: inferred matches, oldest process first.
	// older processes have accumulated more messages and get priority.
	var remaining []processInfo
	for _, p := range processes {
		if _, ok := resolved[p.pid]; !ok && !p.isToolProcess {
			remaining = append(remaining, p)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].startTimeMS < remaining[j].startTimeMS
	})
	for _, proc := range remaining {
		sid := findSessionForProcess(proc, claimed)
		if sid != "" {
			claimed[sid] = true
			resolved[proc.pid] = sid
		}
	}

	// build final pairs preserving original process order
	var correlated []correlatedSession
	for _, proc := range processes {
		sid := resolved[proc.pid]
		var session *sessionInfo
		if sid != "" {
			session = getSessionInfo(sid)
		}
		correlated = append(correlated, correlatedSession{
			process: proc,
			session: session,
		})
	}

	return processes, correlated
}

// fetchAll runs all data collection concurrently.
// correlation + stats + MCP config run in parallel goroutines.
func fetchAll() fetchResult {
	var (
		result fetchResult
		mu     sync.Mutex
		wg     sync.WaitGroup
	)

	wg.Add(3)

	// correlation: ps/lsof + per-session db queries
	go func() {
		defer wg.Done()
		_, correlated := correlateAllSessions()
		mu.Lock()
		result.correlated = correlated
		mu.Unlock()
	}()

	// stats queries
	go func() {
		defer wg.Done()
		today := queryTodayStats()
		global := queryGlobalStats()
		mu.Lock()
		result.todayStats = today
		result.globalStats = global
		mu.Unlock()
	}()

	// MCP config (file I/O, fast but independent)
	go func() {
		defer wg.Done()
		mcp := readMCPConfig()
		mu.Lock()
		result.mcpConfig = mcp
		mu.Unlock()
	}()

	wg.Wait()
	return result
}
