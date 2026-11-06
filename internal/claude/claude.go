package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nono/internal/hub"
	"nono/internal/qoder"
)

const (
	AgentID   = "claude-code"
	AgentName = "Claude Code"

	window     = 30 * time.Minute
	liveWindow = 30 * time.Minute
	recent     = 5
	tailSize   = 2 << 20

	defaultWindow = window
)

type Source struct {
	mu    sync.Mutex
	files map[string]cachedFile
}

func New() *Source { return &Source{files: map[string]cachedFile{}} }

func (s *Source) ID() string   { return AgentID }
func (s *Source) Name() string { return AgentName }

func root() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

type activeSession struct {
	SessionID string
	Cwd       string
	Name      string
	StartedAt int64
	Alive     bool
	Path      string
}

func (s *Source) Running() bool {
	if len(s.active()) > 0 {
		return true
	}
	for _, path := range s.transcripts() {
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) <= liveWindow {
			return true
		}
	}
	return hub.RunningContaining("claude")
}

func (s *Source) Focus() error {
	return hub.FocusContaining("claude", "claude")
}

func (s *Source) active() []activeSession {
	dir := filepath.Join(root(), "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []activeSession
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			StartedAt int64  `json:"startedAt"`
			Name      string `json:"name"`
		}
		if json.Unmarshal(data, &meta) != nil || meta.SessionID == "" {
			continue
		}
		alive := processAlive(meta.PID)
		if !alive {
			continue
		}
		out = append(out, activeSession{
			SessionID: meta.SessionID,
			Cwd:       meta.Cwd,
			Name:      meta.Name,
			StartedAt: meta.StartedAt,
			Alive:     alive,
		})
	}
	return out
}

func (s *Source) transcripts() []string {
	files, _ := filepath.Glob(filepath.Join(root(), "projects", "*", "*.jsonl"))
	return files
}

func (s *Source) Sessions(ctx context.Context) ([]qoder.Session, error) {
	now := time.Now()
	window := hub.Window(defaultWindow)
	active := map[string]activeSession{}
	for _, session := range s.active() {
		active[session.SessionID] = session
	}
	transcripts := map[string]string{}
	for _, path := range s.transcripts() {
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if old, ok := transcripts[id]; !ok || modTime(path).After(modTime(old)) {
			transcripts[id] = path
		}
	}

	var out []qoder.Session
	seen := map[string]bool{}
	for id, path := range transcripts {
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		meta, isActive := active[id]
		if !isActive && now.Sub(st.ModTime()) > window {
			continue
		}
		info := s.parseFile(path, st.Size(), st.ModTime())
		state := qoder.StateIdle
		detail := "空闲"
		if info.hasEntry {
			state, detail = statusOf(info.last, st.ModTime(), now, isActive)
		} else if isActive {
			state, detail = qoder.StateStarting, "启动中"
		}
		last := st.ModTime()
		if info.lastAt.After(last) {
			last = info.lastAt
		}
		if meta.StartedAt > 0 {
			if started := time.UnixMilli(meta.StartedAt); started.After(last) && !info.hasEntry {
				last = started
			}
		}
		if !isActive && now.Sub(last) > window {
			continue
		}
		cwd := info.cwd
		if cwd == "" {
			cwd = meta.Cwd
		}
		title := info.title
		if title == "" {
			title = meta.Name
		}
		if title == "" {
			title = hub.ProjectName(cwd, AgentName)
		}
		out = append(out, qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           id,
			Project:      hub.ProjectName(cwd, AgentName),
			Title:        title,
			Model:        hub.ShortModel(info.model),
			Cwd:          cwd,
			State:        state,
			Detail:       detail,
			LastActivity: last,
			Metrics:      info.metrics,
		})
		seen[id] = true
	}
	for id, meta := range active {
		if seen[id] {
			continue
		}
		started := time.Now()
		if meta.StartedAt > 0 {
			started = time.UnixMilli(meta.StartedAt)
		}
		if now.Sub(started) > window {
			continue
		}
		out = append(out, qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           id,
			Project:      hub.ProjectName(meta.Cwd, AgentName),
			Title:        meta.Name,
			Cwd:          meta.Cwd,
			State:        qoder.StateStarting,
			Detail:       "启动中",
			LastActivity: started,
			Metrics:      qoder.Metrics{},
		})
	}
	return out, nil
}

type claudeContent struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Text string `json:"text"`
}

type claudeEntry struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Summary   string `json:"summary"`
	Message   struct {
		ID         string          `json:"id"`
		Role       string          `json:"role"`
		Model      string          `json:"model"`
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
		Usage      struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (e claudeEntry) content() []claudeContent {
	if len(e.Message.Content) == 0 {
		return nil
	}
	var items []claudeContent
	if json.Unmarshal(e.Message.Content, &items) != nil {
		return nil
	}
	return items
}

func (e claudeEntry) text() string {
	items := e.content()
	for _, item := range items {
		if item.Type == "text" && item.Text != "" {
			return item.Text
		}
	}
	return ""
}

func (e claudeEntry) hasTool() bool {
	for _, item := range e.content() {
		if item.Type == "tool_use" {
			return true
		}
	}
	return e.Message.StopReason == "tool_use"
}

type fileInfo struct {
	size     int64
	mod      time.Time
	cwd      string
	model    string
	title    string
	last     claudeEntry
	lastAt   time.Time
	hasEntry bool
	metrics  qoder.Metrics
}

type cachedFile struct {
	size int64
	mod  time.Time
	info fileInfo
}

func (s *Source) parseFile(path string, size int64, mod time.Time) fileInfo {
	s.mu.Lock()
	if cached, ok := s.files[path]; ok && cached.size == size && cached.mod.Equal(mod) {
		s.mu.Unlock()
		return cached.info
	}
	s.mu.Unlock()

	info := fileInfo{size: size, mod: mod}
	data := readTail(path, tailSize)
	lines := bytes.Split(data, []byte("\n"))

	type accTurn struct {
		start  time.Time
		first  time.Time
		last   time.Time
		tokens map[string]int
	}
	var turns []*accTurn
	var current *accTurn
	flush := func() {
		if current != nil && !current.start.IsZero() && !current.first.IsZero() {
			turns = append(turns, current)
		}
		current = nil
	}

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry claudeEntry
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		at := parseTime(entry.Timestamp)
		if entry.Cwd != "" {
			info.cwd = entry.Cwd
		}
		if entry.Message.Model != "" {
			info.model = entry.Message.Model
		}
		switch entry.Type {
		case "user":
			items := entry.content()
			isToolResult := false
			for _, item := range items {
				if item.Type == "tool_result" {
					isToolResult = true
					break
				}
			}
			if isToolResult || entry.Message.Role == "tool" {
				continue
			}
			if info.title == "" {
				if text := strings.TrimSpace(entry.text()); text != "" {
					info.title = truncate(text, 40)
				}
			}
			flush()
			if !at.IsZero() {
				current = &accTurn{start: at, tokens: map[string]int{}}
			}
			info.last = entry
			info.lastAt = at
			info.hasEntry = true
		case "assistant":
			if info.title == "" {
				if text := strings.TrimSpace(entry.text()); text != "" {
					info.title = truncate(text, 40)
				}
			}
			info.last = entry
			info.lastAt = at
			info.hasEntry = true
			if current == nil || current.start.IsZero() || at.IsZero() {
				continue
			}
			if current.first.IsZero() {
				current.first = at
			}
			if at.After(current.last) {
				current.last = at
			}
			id := entry.Message.ID
			if id == "" {
				id = at.String()
			}
			if usage := entry.Message.Usage.OutputTokens; usage > current.tokens[id] {
				current.tokens[id] = usage
			}
		case "system", "result", "summary":
			if entry.Type == "system" && entry.Subtype == "turn_duration" {
				info.last = entry
				info.lastAt = at
				info.hasEntry = true
			}
		}
	}
	flush()

	var metrics []qoder.TurnMetric
	for i := len(turns) - 1; i >= 0 && len(metrics) < recent; i-- {
		turn := turns[i]
		output := 0
		for _, tokens := range turn.tokens {
			output += tokens
		}
		metric := qoder.TurnMetric{
			TurnID:       turn.start.Format(time.RFC3339Nano),
			StartedAt:    turn.start,
			EndedAt:      turn.last,
			OutputTokens: output,
		}
		metric.TTFT = turn.first.Sub(turn.start)
		metric.PostFirst = turn.last.Sub(turn.first)
		if span := turn.last.Sub(turn.first); span > 0 && output > 0 {
			metric.TokensPerSec = float64(output) / span.Seconds()
		}
		metrics = append(metrics, metric)
	}
	info.metrics = qoder.Metrics{Recent: metrics}
	hub.ComputeAverages(&info.metrics)
	if len(metrics) > 0 && info.last.Type == "assistant" && info.last.Message.StopReason != "end_turn" &&
		time.Since(mod) <= 15*time.Second {
		item := metrics[0]
		item.PostFirst = time.Since(item.StartedAt.Add(item.TTFT))
		info.metrics.Live = &item
	}

	s.mu.Lock()
	s.files[path] = cachedFile{size: size, mod: mod, info: info}
	s.mu.Unlock()
	return info
}

func statusOf(last claudeEntry, mod, now time.Time, active bool) (qoder.State, string) {
	fresh := now.Sub(mod) <= 5*time.Second
	switch last.Type {
	case "assistant":
		if last.hasTool() {
			if fresh {
				name := toolName(last)
				if name != "" {
					return qoder.StateTool, "执行工具 · " + name
				}
				return qoder.StateTool, "执行工具"
			}
			if active {
				return qoder.StatePermission, "等待确认"
			}
			return qoder.StateIdle, "空闲"
		}
		if last.Message.StopReason == "end_turn" {
			return qoder.StateCompleted, "已完成"
		}
		if fresh {
			return qoder.StateThinking, "生成中"
		}
		return qoder.StateCompleted, "已完成"
	case "user":
		if active && fresh {
			return qoder.StateStarting, "启动中"
		}
	case "result":
		return qoder.StateCompleted, "已完成"
	case "system":
		if last.Subtype == "turn_duration" {
			return qoder.StateCompleted, "已完成"
		}
	}
	return qoder.StateIdle, "空闲"
}

func toolName(entry claudeEntry) string {
	for _, item := range entry.content() {
		if item.Type == "tool_use" && item.Name != "" {
			return item.Name
		}
	}
	return ""
}

func readTail(path string, max int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	if st.Size() > max {
		if _, err := f.Seek(st.Size()-max, io.SeekStart); err != nil {
			return nil
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	if st.Size() > max {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data
}

func parseTime(value string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at
}

func truncate(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
