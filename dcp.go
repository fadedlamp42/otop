// DCP plugin state enrichment for /sessions HTTP endpoint.
//
// reads per-session DCP plugin state from disk and exposes key fields
// (manual mode, total pruned tokens, compression block count, etc)
// so rose or any future display layer can reflect plugin activity.
//
// file path matches the fadedlamp42/opencode-dcp fork's persistence layer:
// $HOME/.local/share/opencode/storage/plugin/dcp/<sessionID>.json
//
// the upstream @tarquinen/opencode-dcp doesn't persist manualMode, so
// sessions spawned before the fork was installed return nil for that field.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// dcpState holds the subset of DCP plugin state that OTOP surfaces.
// pointer/empty-string distinguishes "field absent" from "explicit value".
type dcpState struct {
	manualMode         *string // "active" or "off", nil if absent from file
	totalPruneTokens   int64
	pruneBlockCount    int
	lastPruneTopic     string
	lastUpdatedMS      int64
	compressPermission string
}

// persistedDcpState mirrors the fork's PersistedSessionState, keeping only
// the fields OTOP needs. unknown keys are discarded by the JSON decoder.
// manualMode is json.RawMessage because the on-disk value is either the
// bool `false` or the string "active" (union type preserved from TS source).
//
// prune.messages is a STRUCT with several keys, not a map of blocks:
//   - blocksById: map of block id string → full compression block
//   - activeBlockIds: array of block ids currently active (non-decompressed)
//   - byMessageId, activeByAnchorMessageId, nextBlockId, nextRunId (ignored)
//
// a "block" is one compression: range of messages replaced by a summary,
// tagged with a topic, createdAt ms, and an active flag that flips to
// false when the user decompresses it via /dcp decompress.
type persistedPruneBlock struct {
	Topic     string `json:"topic"`
	CreatedAt int64  `json:"createdAt"`
	Active    bool   `json:"active"`
}

type persistedDcpState struct {
	Stats struct {
		TotalPruneTokens int64 `json:"totalPruneTokens"`
	} `json:"stats"`
	Prune struct {
		Messages struct {
			BlocksById map[string]persistedPruneBlock `json:"blocksById"`
			// activeBlockIds is an array of NUMBERS despite blocksById using
			// numeric-string keys (json object keys must be strings; array
			// elements keep their native type).
			ActiveBlockIds []int `json:"activeBlockIds"`
		} `json:"messages"`
	} `json:"prune"`
	LastUpdated        string          `json:"lastUpdated"`
	ManualMode         json.RawMessage `json:"manualMode"`
	CompressPermission string          `json:"compressPermission"`
}

// readDcpState reads and parses the DCP plugin state file for a session.
// returns nil on any error: file missing, permission denied, malformed JSON.
// callers treat nil as "no DCP data available" and omit the field from output.
func readDcpState(sessionID string) *dcpState {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	path := filepath.Join(home, ".local", "share", "opencode", "storage",
		"plugin", "dcp", sessionID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw persistedDcpState
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	state := &dcpState{
		totalPruneTokens:   raw.Stats.TotalPruneTokens,
		pruneBlockCount:    len(raw.Prune.Messages.ActiveBlockIds),
		compressPermission: raw.CompressPermission,
	}

	// coerce manualMode: on-disk value is either `false` bool or `"active"` string.
	// fork's persistence.ts collapses "compress-pending" to "active" on save,
	// so we only ever see these two shapes from the fork's output.
	// absent or null both yield nil.
	if len(raw.ManualMode) > 0 {
		var asString string
		var asBool bool
		if err := json.Unmarshal(raw.ManualMode, &asString); err == nil {
			state.manualMode = &asString
		} else if err := json.Unmarshal(raw.ManualMode, &asBool); err == nil && !asBool {
			off := "off"
			state.manualMode = &off
		}
	}

	// find the most recent ACTIVE block's topic as a "currently compressing" hint.
	// skip inactive blocks so decompressed history doesn't become the display topic.
	// map iteration order is random in Go, so we pick by max createdAt.
	var latestCreatedAt int64
	for _, block := range raw.Prune.Messages.BlocksById {
		if !block.Active {
			continue
		}
		if block.CreatedAt > latestCreatedAt {
			latestCreatedAt = block.CreatedAt
			state.lastPruneTopic = block.Topic
		}
	}

	// parse lastUpdated ISO-8601 into ms for freshness calculation.
	if raw.LastUpdated != "" {
		if t, err := time.Parse(time.RFC3339, raw.LastUpdated); err == nil {
			state.lastUpdatedMS = t.UnixMilli()
		}
	}

	return state
}
