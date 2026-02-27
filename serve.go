// HTTP server mode for feeding data to the Rose companion app.
//
// serves the same correlated session data as the TUI, but as JSON
// over HTTP so the phone can poll it via adb reverse port forwarding.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// serveCommand starts an HTTP server that exposes session data as JSON.
func serveCommand(port int) {
	http.HandleFunc("/sessions", handleSessions)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("otop serve on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("error: %v\n", err)
	}
}

// handleSessions returns the full correlated session list as JSON.
// includes all fields the phone needs: process info, session state,
// last output, tokens, todos, and timestamps for freshness calculation.
func handleSessions(w http.ResponseWriter, r *http.Request) {
	_, correlated := correlateAllSessions()
	todayStats := queryTodayStats()
	globalStats := queryGlobalStats()
	nowMS := time.Now().UnixMilli()

	var sessions []map[string]any
	for _, cs := range correlated {
		if cs.process.isToolProcess || cs.session == nil {
			continue
		}

		status := inferStatus(cs.session, cs.process.cpuPercent)

		uptimeMS := int64(0)
		if cs.process.startTimeMS > 0 {
			uptimeMS = nowMS - cs.process.startTimeMS
		}

		roundMS := int64(0)
		if cs.session.roundStartTime > 0 {
			roundMS = nowMS - cs.session.roundStartTime
		}

		entry := map[string]any{
			"session_id":          cs.session.sessionID,
			"title":               cs.session.title,
			"status":              status,
			"model":               shortModel(cs.session.model),
			"last_output":         cs.session.lastOutput,
			"directory":           cs.session.directory,
			"message_count":       cs.session.messageCount,
			"total_input_tokens":  cs.session.totalInputTokens,
			"total_output_tokens": cs.session.totalOutputTokens,
			"total_cache_read":    cs.session.totalCacheRead,
			"last_message_time":   cs.session.lastMessageTime,
			"uptime_ms":           uptimeMS,
			"round_ms":            roundMS,
			"cpu_percent":         cs.process.cpuPercent,
			"mem_mb":              cs.process.memMB,
			"pid":                 cs.process.pid,
			"tty":                 cs.process.tty,
			"interactive":         cs.session.interactive,
		}

		// include todos if present
		if len(cs.session.activeTodos) > 0 {
			var todos []map[string]string
			for _, t := range cs.session.activeTodos {
				todos = append(todos, map[string]string{
					"content":  t.content,
					"status":   t.status,
					"priority": t.priority,
				})
			}
			entry["todos"] = todos
		}

		sessions = append(sessions, entry)
	}

	response := map[string]any{
		"timestamp": nowMS,
		"sessions":  sessions,
		"today": map[string]any{
			"session_count": todayStats.sessionCount,
			"message_count": todayStats.messageCount,
			"total_input":   todayStats.totalInput,
			"total_output":  todayStats.totalOutput,
		},
		"global": map[string]any{
			"session_count": globalStats.sessionCount,
			"message_count": globalStats.messageCount,
			"total_input":   globalStats.totalInput,
			"total_output":  globalStats.totalOutput,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}
