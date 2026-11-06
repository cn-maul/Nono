package workbuddy

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nono/internal/hub"
	"nono/internal/qoder"
)

const (
	AgentID   = "workbuddy"
	AgentName = "WorkBuddy"

	window = 30 * time.Minute

	staleLive = 10 * time.Minute

	recent = 5

	defaultWindow = window
)

type Source struct {
	mu   sync.Mutex
	db   *sql.DB
	logs map[string]*logCache
}

func New() *Source { return &Source{logs: map[string]*logCache{}} }

func (s *Source) ID() string   { return AgentID }
func (s *Source) Name() string { return AgentName }

func (s *Source) Running() bool {
	return hub.RunningContaining("workbuddy")
}

func (s *Source) Focus() error {
	return hub.FocusContaining("workbuddy", "workbuddy")
}

func (s *Source) store() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	home, _ := os.UserHomeDir()
	db, err := hub.OpenReadOnly(filepath.Join(home, ".workbuddy", "workbuddy.db"))
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *Source) Sessions(ctx context.Context) ([]qoder.Session, error) {
	db, err := s.store()
	if err != nil {
		return nil, err
	}
	s.refreshLogs()
	const query = `
		select id, coalesce(nullif(custom_title, ''), title, ''), cwd, status,
		       coalesce(model, ''), coalesce(last_activity_at, updated_at, created_at)
		from sessions
		where status <> 'archived'
		order by coalesce(last_activity_at, updated_at, created_at) desc
		limit 20`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	window := hub.Window(defaultWindow)
	var out []qoder.Session
	for rows.Next() {
		var id, title, cwd, status, model string
		var updated int64
		if err := rows.Scan(&id, &title, &cwd, &status, &model, &updated); err != nil {
			return nil, err
		}
		last := time.UnixMilli(updated)
		state, detail := stateOf(status)
		if state.Active() && now.Sub(last) > staleLive {
			state, detail = qoder.StateIdle, "空闲"
		}
		if !state.Active() && now.Sub(last) > window {
			continue
		}
		metrics := s.logMetrics(id, last)
		if len(metrics.Recent) > 0 && metrics.Recent[0].EndedAt.After(last) {
			last = metrics.Recent[0].EndedAt
		}
		if title == "" {
			title = hub.ProjectName(cwd, AgentName)
		}
		if model != "" {
			model = hub.ShortModel(model)
		}
		out = append(out, qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           id,
			Project:      AgentName,
			Title:        title,
			Model:        model,
			Cwd:          cwd,
			State:        state,
			Detail:       detail,
			LastActivity: last,
			Metrics:      metrics,
		})
	}
	return out, rows.Err()
}

type modelCall struct {
	EventID    string `json:"event_id"`
	Model      string `json:"model"`
	Step       int    `json:"step"`
	TTFT       int64  `json:"time_to_first_token_ms"`
	Input      int    `json:"input_tokens"`
	Output     int    `json:"output_tokens"`
	Duration   int64  `json:"duration_ms"`
	RequestID  string `json:"conversation_request_id"`
	Completion string `json:"conversation_message_id"`
	SessionID  string `json:"conversation_id"`
	At         time.Time
}

var wbStamp = regexp.MustCompile(`^\[(\d{4})年(\d{1,2})月(\d{1,2})日 (上午|下午)(\d{1,2}):(\d{2}):(\d{2})\.(\d{3})\]`)

type logCache struct {
	offset  int64
	partial []byte
	seen    map[string]bool
	calls   map[string][]modelCall
}

func (s *Source) refreshLogs() {
	home, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(home, ".workbuddy", "logs", "*", "*.log"))
	now := time.Now()
	for _, path := range files {
		st, err := os.Stat(path)
		if err != nil || now.Sub(st.ModTime()) > 48*time.Hour {
			continue
		}
		cache := s.logs[path]
		if cache == nil {
			cache = &logCache{seen: map[string]bool{}, calls: map[string][]modelCall{}}
			s.logs[path] = cache
		}
		if st.Size() < cache.offset {
			cache.offset = 0
			cache.partial = nil
			cache.seen = map[string]bool{}
			cache.calls = map[string][]modelCall{}
		}
		if st.Size() == cache.offset {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(cache.offset, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			continue
		}
		cache.offset += int64(len(data))
		chunk := append(cache.partial, data...)
		next := strings.LastIndexByte(string(chunk), '\n')
		if next < 0 {
			cache.partial = chunk
			continue
		}
		cache.partial = append(cache.partial[:0], chunk[next+1:]...)
		for _, line := range strings.Split(string(chunk[:next+1]), "\n") {
			parseModelCall(strings.TrimSpace(line), cache)
		}
	}
}

func parseModelCall(line string, cache *logCache) {
	if !strings.Contains(line, `"event_name":"agent.model_call.completed"`) {
		return
	}
	start := strings.IndexByte(line, '{')
	if start < 0 {
		return
	}
	var call modelCall
	if json.Unmarshal([]byte(line[start:]), &call) != nil || call.SessionID == "" {
		return
	}
	if call.EventID != "" {
		if cache.seen[call.EventID] {
			return
		}
		cache.seen[call.EventID] = true
	}
	call.At = parseWBStamp(line)
	cache.calls[call.SessionID] = append(cache.calls[call.SessionID], call)
}

func parseWBStamp(line string) time.Time {
	match := wbStamp.FindStringSubmatch(line)
	if match == nil {
		return time.Time{}
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	hour, _ := strconv.Atoi(match[5])
	minute, _ := strconv.Atoi(match[6])
	second, _ := strconv.Atoi(match[7])
	milli, _ := strconv.Atoi(match[8])
	hour %= 12
	if match[4] == "下午" {
		hour += 12
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), time.Local)
}

func (s *Source) logMetrics(sessionID string, fallback time.Time) qoder.Metrics {
	var calls []modelCall
	seen := map[string]bool{}
	for _, cache := range s.logs {
		for _, call := range cache.calls[sessionID] {
			key := call.RequestID + "|" + strconv.Itoa(call.Step)
			if call.EventID != "" {
				key = call.EventID
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			calls = append(calls, call)
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].At.After(calls[j].At) })

	var turns []qoder.TurnMetric
	for _, call := range calls {
		if len(turns) >= recent {
			break
		}
		at := call.At
		if at.IsZero() {
			at = fallback
		}
		duration := time.Duration(call.Duration) * time.Millisecond
		start := at.Add(-duration)
		turn := qoder.TurnMetric{
			TurnID:       call.RequestID + ":" + strconv.Itoa(call.Step),
			StartedAt:    start,
			EndedAt:      at,
			TTFT:         time.Duration(call.TTFT) * time.Millisecond,
			OutputTokens: call.Output,
		}
		if duration > 0 {
			turn.PostFirst = duration - turn.TTFT
			if turn.PostFirst < 0 {
				turn.PostFirst = 0
			}
			turn.TokensPerSec = float64(call.Output) / duration.Seconds()
		}
		turns = append(turns, turn)
	}
	metrics := qoder.Metrics{Recent: turns}
	hub.ComputeAverages(&metrics)
	return metrics
}

func stateOf(status string) (qoder.State, string) {
	switch strings.ToLower(status) {
	case "working", "running", "in_progress", "generating":
		return qoder.StateThinking, "生成中"
	case "pending", "queued", "starting":
		return qoder.StateStarting, "启动中"
	case "completed", "done":
		return qoder.StateCompleted, "已完成"
	case "error", "failed":
		return qoder.StateFailed, "失败"
	case "cancelled", "canceled", "stopped", "interrupted":
		return qoder.StateInterrupted, "已中断"
	}
	return qoder.StateIdle, "空闲"
}
