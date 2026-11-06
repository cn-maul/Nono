package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxInitialParse = 24 << 20
	tailParse       = 8 << 20
)

type turnRollout struct {
	Started        time.Time
	Ended          time.Time
	FirstOutput    time.Time
	FirstToken     time.Time
	LastToken      time.Time
	OutputTokens   int
	LastItemType   string
	LastItemDetail string
}

type rolloutStats struct {
	Cwd            string
	Model          string
	Turns          map[string]*turnRollout
	LastTurn       string
	LastEvent      string
	LastEventAt    time.Time
	LastItemType   string
	LastItemDetail string
	LastItemAt     time.Time

	totalOutput int
	hasTotal    bool
}

func newRolloutStats() rolloutStats {
	return rolloutStats{Turns: map[string]*turnRollout{}}
}

type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type rolloutPayload struct {
	Type        string `json:"type"`
	TurnID      string `json:"turn_id"`
	Cwd         string `json:"cwd"`
	Model       string `json:"model"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at"`
	StartedAtMs int64  `json:"started_at_ms"`
	Info        struct {
		TotalTokenUsage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"total_token_usage"`
	} `json:"info"`
	Item struct {
		Type    string   `json:"type"`
		Command []string `json:"command"`
	} `json:"item"`
}

func (s *rolloutStats) turn(id string) *turnRollout {
	if id == "" {
		return nil
	}
	turn, ok := s.Turns[id]
	if !ok {
		turn = &turnRollout{}
		s.Turns[id] = turn
	}
	return turn
}

func (s *rolloutStats) apply(line []byte) {
	var ev rolloutLine
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	at := parseStamp(ev.Timestamp)
	switch ev.Type {
	case "session_meta", "turn_context":
		var p rolloutPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return
		}
		if s.Cwd == "" && p.Cwd != "" {
			s.Cwd = p.Cwd
		}
		if s.Model == "" && p.Model != "" {
			s.Model = p.Model
		}
	case "event_msg":
		s.applyEvent(ev.Payload, at)
	}
}

func (s *rolloutStats) applyEvent(raw json.RawMessage, at time.Time) {
	var p rolloutPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	switch p.Type {
	case "task_started":
		turn := s.turn(p.TurnID)
		if turn != nil {
			turn.Started = time.Unix(p.StartedAt, 0)
			if p.StartedAt <= 0 {
				turn.Started = at
			}
		}
		s.LastTurn = p.TurnID
		s.LastEvent = "task_started"
		s.LastEventAt = at
	case "task_complete":
		turn := s.turn(p.TurnID)
		if turn != nil {
			turn.Ended = at
		}
		if p.TurnID != "" {
			s.LastTurn = p.TurnID
		}
		s.LastEvent = "task_complete"
		s.LastEventAt = at
	case "turn_aborted":
		turn := s.turn(p.TurnID)
		if turn != nil {
			turn.Ended = at
			if p.CompletedAt > 0 {
				turn.Ended = time.Unix(p.CompletedAt, 0)
			}
		}
		if p.TurnID != "" {
			s.LastTurn = p.TurnID
		}
		s.LastEvent = "turn_aborted"
		s.LastEventAt = at
	case "token_count":
		total := p.Info.TotalTokenUsage.OutputTokens
		turn := s.turn(s.LastTurn)
		switch {
		case !s.hasTotal:
			s.totalOutput = total
			s.hasTotal = true
		case total >= s.totalOutput:
			if turn != nil {
				turn.OutputTokens += total - s.totalOutput
			}
			s.totalOutput = total
		default:
			s.totalOutput = total
		}
		if turn != nil {
			if turn.FirstToken.IsZero() {
				turn.FirstToken = at
			}
			turn.LastToken = at
		}
	case "item_completed":
		turn := s.turn(p.TurnID)
		if turn == nil {
			return
		}
		turn.LastItemType = p.Item.Type
		turn.LastItemDetail = itemDetail(p.Item.Type, p.Item.Command)
		if turn.FirstOutput.IsZero() && (p.Item.Type == "Reasoning" || p.Item.Type == "AgentMessage") {
			switch {
			case p.StartedAtMs > 0:
				turn.FirstOutput = time.UnixMilli(p.StartedAtMs)
			case !at.IsZero():
				turn.FirstOutput = at
			}
		}
		s.LastItemType = turn.LastItemType
		s.LastItemDetail = turn.LastItemDetail
		s.LastItemAt = at
	}
}

func itemDetail(kind string, command []string) string {
	switch kind {
	case "CommandExecution":
		if len(command) > 0 {
			return strings.TrimSuffix(filepath.Base(command[0]), ".exe")
		}
	case "FileChange":
		return "文件修改"
	case "McpToolCall":
		return "MCP 工具"
	}
	return ""
}

func parseStamp(value string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at
}

// rolloutReader incrementally parses one rollout file, so polling stays cheap
// while a turn appends thousands of events.
type rolloutReader struct {
	path        string
	offset      int64
	partial     []byte
	initialized bool
	stats       rolloutStats
}

func newRolloutReader(path string) *rolloutReader {
	return &rolloutReader{path: path, stats: newRolloutStats()}
}

func (r *rolloutReader) refresh() {
	f, err := os.Open(r.path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	if !r.initialized {
		r.initialized = true
		if st.Size() > maxInitialParse {
			r.seedTail(f, st.Size())
		}
	}
	if st.Size() < r.offset {
		r.offset = 0
		r.partial = nil
		r.stats = newRolloutStats()
	}
	if st.Size() == r.offset {
		return
	}
	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return
	}
	r.offset += int64(len(data))
	chunk := append(r.partial, data...)
	next := bytes.LastIndexByte(chunk, '\n')
	if next < 0 {
		r.partial = chunk
		return
	}
	r.partial = append(r.partial[:0], chunk[next+1:]...)
	for _, line := range bytes.Split(chunk[:next+1], []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		r.stats.apply(line)
	}
}

// seedTail starts a huge file near its end, skipping the partial first line
// and seeding the token counter so the first delta is not the whole history.
func (r *rolloutReader) seedTail(f *os.File, size int64) {
	offset := size - tailParse
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return
	}
	buf := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, buf)
	if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
		r.offset = offset + int64(i) + 1
	} else {
		r.offset = offset
	}
	r.stats.hasTotal = false
}
