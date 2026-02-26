// sqlite queries against opencode's database.
//
// all queries are read-only (?mode=ro). safe to run concurrently with
// active opencode instances writing in WAL mode.

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// openDB opens a read-only connection to the opencode sqlite database.
func openDB() (*sql.DB, error) {
	path := dbPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+path+"?mode=ro")
}

// getSessionInfo fetches full session data including message aggregates.
// returns nil if the session doesn't exist or on any error.
func getSessionInfo(sessionID string) *sessionInfo {
	db, err := openDB()
	if err != nil {
		return nil
	}
	defer db.Close()

	var (
		sid, title, directory, projectID, version sql.NullString
		permission                                sql.NullString
		sesCreated, sesUpdated                    sql.NullInt64
		msgCount                                  sql.NullInt64
		totalContext, totalOutput, totalCache     sql.NullInt64
		totalCost                                 sql.NullFloat64
	)

	err = db.QueryRow(`
		SELECT
			s.id, s.title, s.directory, s.project_id, s.version,
			s.permission,
			s.time_created, s.time_updated,
			count(m.id),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN coalesce(json_extract(m.data, '$.tokens.input'), 0)
				   + coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
				ELSE 0 END),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN json_extract(m.data, '$.tokens.output') ELSE 0 END),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
				ELSE 0 END),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN json_extract(m.data, '$.cost') ELSE 0 END)
		FROM session s
		LEFT JOIN message m ON m.session_id = s.id
		WHERE s.id = ?
		GROUP BY s.id
	`, sessionID).Scan(
		&sid, &title, &directory, &projectID, &version,
		&permission,
		&sesCreated, &sesUpdated,
		&msgCount,
		&totalContext, &totalOutput, &totalCache, &totalCost,
	)
	if err != nil {
		return nil
	}

	titleStr := title.String
	if titleStr == "" {
		titleStr = "(untitled)"
	}

	session := &sessionInfo{
		sessionID:         sid.String,
		title:             titleStr,
		directory:         directory.String,
		projectID:         projectID.String,
		version:           version.String,
		interactive:       !permission.Valid,
		timeCreated:       sesCreated.Int64,
		timeUpdated:       sesUpdated.Int64,
		messageCount:      int(msgCount.Int64),
		totalInputTokens:  totalContext.Int64,
		totalOutputTokens: totalOutput.Int64,
		totalCacheRead:    totalCache.Int64,
		totalCost:         totalCost.Float64,
	}

	// last message: determines current state (role, finish, model, agent)
	var lastRole, lastFinish, lastModel, lastAgent sql.NullString
	var lastMsgTime sql.NullInt64
	err = db.QueryRow(`
		SELECT
			json_extract(data, '$.role'),
			json_extract(data, '$.finish'),
			json_extract(data, '$.modelID'),
			json_extract(data, '$.agent'),
			time_created
		FROM message
		WHERE session_id = ?
		ORDER BY time_created DESC
		LIMIT 1
	`, sessionID).Scan(&lastRole, &lastFinish, &lastModel, &lastAgent, &lastMsgTime)
	if err == nil {
		session.lastMessageRole = lastRole.String
		if session.lastMessageRole == "" {
			session.lastMessageRole = "?"
		}
		if lastFinish.Valid {
			s := lastFinish.String
			session.lastFinish = &s
		}
		if lastModel.Valid && lastModel.String != "" {
			session.model = lastModel.String
		} else {
			session.model = "?"
		}
		if lastAgent.Valid && lastAgent.String != "" {
			session.agent = lastAgent.String
		} else {
			session.agent = "?"
		}
		session.lastMessageTime = lastMsgTime.Int64
	}

	// round start: most recent user message timestamp
	var roundTime sql.NullInt64
	_ = db.QueryRow(`
		SELECT time_created FROM message
		WHERE session_id = ?
		  AND json_extract(data, '$.role') = 'user'
		ORDER BY time_created DESC
		LIMIT 1
	`, sessionID).Scan(&roundTime)
	session.roundStartTime = roundTime.Int64

	// last output: last non-empty line from the most recent assistant text part
	var lastPartData sql.NullString
	_ = db.QueryRow(`
		SELECT p.data
		FROM part p
		JOIN message m ON p.message_id = m.id
		WHERE p.session_id = ?
		  AND json_extract(m.data, '$.role') = 'assistant'
		  AND json_extract(p.data, '$.type') = 'text'
		ORDER BY p.time_created DESC
		LIMIT 1
	`, sessionID).Scan(&lastPartData)
	if lastPartData.Valid {
		var partObj map[string]any
		if json.Unmarshal([]byte(lastPartData.String), &partObj) == nil {
			if text, ok := partObj["text"].(string); ok {
				text = strings.TrimSpace(text)
				for _, line := range reverseLines(text) {
					line = strings.TrimSpace(line)
					if line != "" {
						session.lastOutput = line
						break
					}
				}
			}
		}
	}

	// todos for the 't' panel
	todoRows, err := db.Query(`
		SELECT content, status, priority
		FROM todo
		WHERE session_id = ?
		ORDER BY position
	`, sessionID)
	if err == nil {
		defer todoRows.Close()
		for todoRows.Next() {
			var content, status, priority string
			if todoRows.Scan(&content, &status, &priority) == nil {
				session.activeTodos = append(session.activeTodos, todoItem{
					content:  content,
					status:   status,
					priority: priority,
				})
			}
		}
	}

	return session
}

// reverseLines splits text into lines and returns them last-to-first.
func reverseLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// findSessionForProcess correlates a process to its most likely session.
// tier 2: match by cwd + message activity since process start time.
// tier 3: fallback to most recently updated session for the cwd.
func findSessionForProcess(proc processInfo, claimed map[string]bool) string {
	if proc.sessionID != "" {
		return proc.sessionID
	}
	if proc.cwd == "" || proc.cwd == "?" {
		return ""
	}

	db, err := openDB()
	if err != nil {
		return ""
	}
	defer db.Close()

	// tier 2: message-activity-since-start correlation
	if proc.startTimeMS > 0 {
		rows, err := db.Query(`
			SELECT s.id, count(m.id) as msgs_since
			FROM session s
			JOIN message m ON m.session_id = s.id
			WHERE s.directory = ?
			  AND m.time_created >= ?
			GROUP BY s.id
			ORDER BY msgs_since DESC
			LIMIT 5
		`, proc.cwd, proc.startTimeMS)
		if err == nil {
			for rows.Next() {
				var id string
				var count int
				if rows.Scan(&id, &count) == nil && !claimed[id] {
					rows.Close()
					return id
				}
			}
			rows.Close()
		}
	}

	// tier 3: most recently updated session for this directory
	rows, err := db.Query(`
		SELECT id FROM session
		WHERE directory = ?
		ORDER BY time_updated DESC
		LIMIT 5
	`, proc.cwd)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !claimed[id] {
			return id
		}
	}

	return ""
}

// queryTodayStats fetches aggregate stats for sessions active today.
func queryTodayStats() aggStats {
	db, err := openDB()
	if err != nil {
		return aggStats{}
	}
	defer db.Close()

	today := time.Now().Truncate(24 * time.Hour)
	todayMS := today.UnixMilli()

	var sessionCount, messageCount sql.NullInt64
	var totalIn, totalOut sql.NullInt64

	err = db.QueryRow(`
		SELECT
			count(DISTINCT s.id),
			count(m.id),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN coalesce(json_extract(m.data, '$.tokens.input'), 0)
				   + coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
				ELSE 0 END),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN json_extract(m.data, '$.tokens.output') ELSE 0 END)
		FROM session s
		LEFT JOIN message m ON m.session_id = s.id
		WHERE s.time_updated > ?
	`, todayMS).Scan(&sessionCount, &messageCount, &totalIn, &totalOut)
	if err != nil {
		return aggStats{}
	}

	return aggStats{
		sessionCount: int(sessionCount.Int64),
		messageCount: int(messageCount.Int64),
		totalInput:   totalIn.Int64,
		totalOutput:  totalOut.Int64,
	}
}

// queryGlobalStats fetches aggregate stats across all sessions.
func queryGlobalStats() aggStats {
	db, err := openDB()
	if err != nil {
		return aggStats{}
	}
	defer db.Close()

	var sessionCount, messageCount sql.NullInt64
	var totalIn, totalOut sql.NullInt64

	err = db.QueryRow(`
		SELECT
			count(DISTINCT s.id),
			count(m.id),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN coalesce(json_extract(m.data, '$.tokens.input'), 0)
				   + coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
				ELSE 0 END),
			sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
				THEN json_extract(m.data, '$.tokens.output') ELSE 0 END)
		FROM session s
		LEFT JOIN message m ON m.session_id = s.id
	`).Scan(&sessionCount, &messageCount, &totalIn, &totalOut)
	if err != nil {
		return aggStats{}
	}

	return aggStats{
		sessionCount: int(sessionCount.Int64),
		messageCount: int(messageCount.Int64),
		totalInput:   totalIn.Int64,
		totalOutput:  totalOut.Int64,
	}
}

// readMCPConfig reads MCP server definitions from global opencode.json.
func readMCPConfig() map[string]any {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return nil
	}
	var config map[string]any
	if json.Unmarshal(data, &config) != nil {
		return nil
	}
	if mcp, ok := config["mcp"].(map[string]any); ok {
		return mcp
	}
	return nil
}

// getRecentMessages fetches recent messages for the detail view.
// returns messages in chronological order (oldest first).
func getRecentMessages(sessionID string, limit int) []messageDetail {
	db, err := openDB()
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT data, time_created
		FROM message
		WHERE session_id = ?
		ORDER BY time_created DESC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var messages []messageDetail
	for rows.Next() {
		var dataStr string
		var timeCreated int64
		if rows.Scan(&dataStr, &timeCreated) != nil {
			continue
		}
		var d map[string]any
		if json.Unmarshal([]byte(dataStr), &d) != nil {
			continue
		}

		msg := messageDetail{
			role:        jsonStr(d, "role"),
			finish:      jsonStr(d, "finish"),
			model:       jsonStr(d, "modelID"),
			tokensIn:    jsonInt(d, "tokens", "input"),
			tokensOut:   jsonInt(d, "tokens", "output"),
			cacheRead:   jsonInt(d, "tokens", "cache", "read"),
			timeCreated: timeCreated,
		}

		// fetch first text part for preview
		var partData sql.NullString
		err := db.QueryRow(`
			SELECT p.data FROM part p
			JOIN message m ON p.message_id = m.id
			WHERE p.session_id = ?
			  AND m.time_created = ?
			  AND json_extract(p.data, '$.type') = 'text'
			ORDER BY p.time_created ASC
			LIMIT 1
		`, sessionID, timeCreated).Scan(&partData)
		if err == nil && partData.Valid {
			var partObj map[string]any
			if json.Unmarshal([]byte(partData.String), &partObj) == nil {
				if text, ok := partObj["text"].(string); ok {
					if len(text) > 200 {
						text = text[:200]
					}
					msg.textPreview = text
				}
			}
		}

		messages = append(messages, msg)
	}

	// reverse for chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages
}

// -- json helpers --

// jsonStr extracts a string from a nested JSON map.
func jsonStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// jsonInt extracts an int64 from a nested JSON map path.
func jsonInt(m map[string]any, keys ...string) int64 {
	current := m
	for i, key := range keys {
		if i == len(keys)-1 {
			if v, ok := current[key].(float64); ok {
				return int64(v)
			}
			return 0
		}
		if sub, ok := current[key].(map[string]any); ok {
			current = sub
		} else {
			return 0
		}
	}
	return 0
}
