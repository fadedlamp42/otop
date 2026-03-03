// PID-to-session correlation via the otop opencode plugin.
//
// session IDs come from PID files written by the plugin. processes
// without a PID file show as unmatched (no heuristic fallback).
//
// also contains fetchAll() which runs all data collection concurrently.

package main

import (
	"sync"
)

// correlateAllSessions pairs each opencode process with its session.
// session IDs are set on processInfo by readSessionFromPidFile during
// process discovery; this function just looks up the session data from the db.
func correlateAllSessions() ([]processInfo, []correlatedSession) {
	processes := getOpencodeProcesses()

	var correlated []correlatedSession
	for _, proc := range processes {
		var session *sessionInfo
		if proc.sessionID != "" && !proc.isToolProcess {
			session = getSessionInfo(proc.sessionID)
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
