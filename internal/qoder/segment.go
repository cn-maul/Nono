package qoder

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type segmentResponse struct {
	RequestID    string
	At           time.Time
	OutputTokens int
}

type segmentFinish struct {
	At       time.Time
	Duration time.Duration
	Reason   string
}

type SegmentStats struct {
	TurnStart    map[string]time.Time
	Requests     map[string]time.Time
	Responses    map[string][]segmentResponse
	Finished     map[string]segmentFinish
	LastEvent    string
	LastEventAt  time.Time
	LastToolName string
	LastTurnID   string
	PermissionAt time.Time
}

func newSegmentStats() *SegmentStats {
	return &SegmentStats{
		TurnStart: map[string]time.Time{},
		Requests:  map[string]time.Time{},
		Responses: map[string][]segmentResponse{},
		Finished:  map[string]segmentFinish{},
	}
}

type segmentEvent struct {
	TS        string `json:"ts"`
	Type      string `json:"type"`
	TurnID    string `json:"turn_id"`
	RequestID string `json:"request_id"`
	Data      struct {
		OutputTokens int    `json:"output_tokens"`
		DurationMS   int64  `json:"duration_ms"`
		Reason       string `json:"reason"`
		ToolName     string `json:"tool_name"`
		RequestIndex int    `json:"request_index"`
	} `json:"data"`
}

func parseSegmentFile(path string) (*SegmentStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	stats := newSegmentStats()
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var ev segmentEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ev.TS)
		if err != nil {
			continue
		}
		switch ev.Type {
		case "turn.started", "input.prompt.submitted":
			if ev.TurnID != "" {
				stats.TurnStart[ev.TurnID] = at
			}
		case "model.request.started":
			if ev.RequestID != "" {
				stats.Requests[ev.RequestID] = at
			}
			stats.note(ev.Type, at, ev.TurnID, "")
		case "model.response.completed":
			stats.Responses[ev.TurnID] = append(stats.Responses[ev.TurnID], segmentResponse{
				RequestID:    ev.RequestID,
				At:           at,
				OutputTokens: ev.Data.OutputTokens,
			})
			stats.note(ev.Type, at, ev.TurnID, "")
		case "turn.finished":
			stats.Finished[ev.TurnID] = segmentFinish{
				At:       at,
				Duration: time.Duration(ev.Data.DurationMS) * time.Millisecond,
				Reason:   ev.Data.Reason,
			}
			stats.note(ev.Type, at, ev.TurnID, "")
		case "tool.requested", "tool.shell.started", "tool.execution.finished":
			stats.note(ev.Type, at, ev.TurnID, ev.Data.ToolName)
		case "permission.resolved":
			stats.PermissionAt = at
		}
	}
	return stats, nil
}

func (s *SegmentStats) note(kind string, at time.Time, turnID, tool string) {
	if at.Before(s.LastEventAt) {
		return
	}
	s.LastEvent = kind
	s.LastEventAt = at
	s.LastTurnID = turnID
	if tool != "" {
		s.LastToolName = tool
	}
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(data) {
		line := data[start:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		out = append(out, line)
	}
	return out
}

func mergeSegmentStats(dst, src *SegmentStats) {
	for id, at := range src.TurnStart {
		dst.TurnStart[id] = at
	}
	for id, at := range src.Requests {
		dst.Requests[id] = at
	}
	for id, responses := range src.Responses {
		dst.Responses[id] = append(dst.Responses[id], responses...)
	}
	for id, finish := range src.Finished {
		dst.Finished[id] = finish
	}
	if src.LastEventAt.After(dst.LastEventAt) {
		dst.LastEvent = src.LastEvent
		dst.LastEventAt = src.LastEventAt
		dst.LastToolName = src.LastToolName
		dst.LastTurnID = src.LastTurnID
	}
	if src.PermissionAt.After(dst.PermissionAt) {
		dst.PermissionAt = src.PermissionAt
	}
}

func cloneSegmentStats(src *SegmentStats) *SegmentStats {
	dst := newSegmentStats()
	if src == nil {
		return dst
	}
	mergeSegmentStats(dst, src)
	return dst
}

func (s *SegmentStats) StartAt(turnID string) (time.Time, bool) {
	at, ok := s.TurnStart[turnID]
	return at, ok
}

// PendingIsQuestion recognizes AskUserQuestion-like tools, where Qoder waits
// for an answer rather than for a permission decision.
func (s *SegmentStats) PendingIsQuestion() bool {
	name := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s.LastToolName))
	switch name {
	case "askuserquestion", "askquestion", "requestuserinput", "requestinput", "question":
		return true
	}
	return false
}

// LatestTurnStart returns the start of the newest turn in the segment logs.
// Its turn IDs are a different namespace from the database's, so the
// collector uses the newest start time for the live turn.
func (s *SegmentStats) LatestTurnStart() (time.Time, bool) {
	var latest time.Time
	for _, at := range s.TurnStart {
		if at.After(latest) {
			latest = at
		}
	}
	return latest, !latest.IsZero()
}

// TokenRateWindow sums the token usage of model responses that completed
// inside a turn's time window. Segment turn IDs and database turn IDs are
// different identifiers, so time is the reliable join key.
func (s *SegmentStats) TokenRateWindow(start, end time.Time) (tps float64, tokens int) {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0, 0
	}
	from := start.Add(-time.Second)
	to := end.Add(2 * time.Second)
	var elapsed time.Duration
	for _, responses := range s.Responses {
		for _, response := range responses {
			if response.At.Before(from) || response.At.After(to) {
				continue
			}
			requestStart, ok := s.Requests[response.RequestID]
			if !ok {
				continue
			}
			d := response.At.Sub(requestStart)
			if d <= 0 {
				continue
			}
			elapsed += d
			tokens += response.OutputTokens
		}
	}
	if elapsed > 0 {
		tps = float64(tokens) / elapsed.Seconds()
	}
	return tps, tokens
}

type fileCache struct {
	size  int64
	mod   time.Time
	stats *SegmentStats
}

type SegmentReader struct {
	roots Roots
	mu    sync.Mutex
	cache map[string]*fileCache
}

func NewSegmentReader(roots Roots) *SegmentReader {
	return &SegmentReader{roots: roots, cache: map[string]*fileCache{}}
}

func (r *SegmentReader) Stats(sessionID string) *SegmentStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots.SessionRoot == "" || sessionID == "" {
		return newSegmentStats()
	}
	pattern := filepath.Join(r.roots.SessionRoot, "*", sessionID, "segments", "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return newSegmentStats()
	}
	type fileInfo struct {
		path string
		mod  time.Time
		size int64
	}
	infos := make([]fileInfo, 0, len(files))
	for _, path := range files {
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		infos = append(infos, fileInfo{path: path, mod: st.ModTime(), size: st.Size()})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].mod.After(infos[j].mod) })
	if len(infos) > 3 {
		infos = infos[:3]
	}

	merged := newSegmentStats()
	for _, info := range infos {
		cached := r.cache[info.path]
		if cached == nil || cached.size != info.size || !cached.mod.Equal(info.mod) {
			stats, err := parseSegmentFile(info.path)
			if err != nil {
				continue
			}
			cached = &fileCache{size: info.size, mod: info.mod, stats: stats}
			r.cache[info.path] = cached
		}
		mergeSegmentStats(merged, cached.stats)
	}
	return merged
}
