package dsh

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nono/internal/hub"
	"nono/internal/qoder"
)

const (
	AgentID   = "deepseek-harness"
	AgentName = "DeepSeek Harness"

	window    = 30 * time.Minute
	staleLive = 10 * time.Minute
	recent    = 5

	defaultWindow = window
)

type Source struct {
	mu sync.Mutex
	db *sql.DB
}

func New() *Source { return &Source{} }

func (s *Source) ID() string   { return AgentID }
func (s *Source) Name() string { return AgentName }

func (s *Source) Running() bool { return hub.RunningContaining("deepseek harness") }

func (s *Source) Focus() error {
	return hub.FocusContaining("deepseek harness", "deepseek harness", "deepseek")
}

func (s *Source) store() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	home, _ := os.UserHomeDir()
	db, err := hub.OpenReadOnly(filepath.Join(home, ".dsh", "sessions", "sessions.sqlite"))
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

type lastEvent struct {
	ID   string
	Cwd  string
	At   time.Time
	Kind string
	Name string
	Data string
}

func (s *Source) Sessions(ctx context.Context) ([]qoder.Session, error) {
	db, err := s.store()
	if err != nil {
		return nil, err
	}
	const query = `
		select s.f_session_id, coalesce(s.f_cwd, ''), e.f_kind, coalesce(e.f_name, ''),
		       coalesce(e.f_data, ''), e.f_created_at
		from t_sessions s
		join t_session_events se on se.f_session_id = s.f_session_id
		join t_events e on e.f_event_id = se.f_event_id
		where se.f_sequence = (
			select max(x.f_sequence) from t_session_events x where x.f_session_id = s.f_session_id
		)
		order by e.f_created_at desc
		limit 20`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	window := hub.Window(defaultWindow)
	var events []lastEvent
	for rows.Next() {
		var ev lastEvent
		var at int64
		if err := rows.Scan(&ev.ID, &ev.Cwd, &ev.Kind, &ev.Name, &ev.Data, &at); err != nil {
			return nil, err
		}
		ev.At = time.UnixMilli(at)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []qoder.Session
	for _, ev := range events {
		state, detail := stateOf(ev, now)
		live := state.Active()
		if !live && now.Sub(ev.At) > window {
			continue
		}
		title := s.title(ctx, db, ev.ID)
		if title == "" {
			title = hub.ProjectName(ev.Cwd, AgentName)
		}
		metrics := s.metrics(ctx, db, ev.ID, live, now)
		out = append(out, qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           ev.ID,
			Project:      hub.ProjectName(ev.Cwd, AgentName),
			Title:        title,
			Cwd:          ev.Cwd,
			State:        state,
			Detail:       detail,
			LastActivity: ev.At,
			Metrics:      metrics,
		})
	}
	return out, nil
}

func stateOf(ev lastEvent, now time.Time) (qoder.State, string) {
	fresh := now.Sub(ev.At) <= staleLive
	switch ev.Kind {
	case "turn/end":
		var data struct {
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		_ = json.Unmarshal([]byte(ev.Data), &data)
		switch data.Reason.Kind {
		case "completed":
			return qoder.StateCompleted, "已完成"
		case "failed", "error":
			return qoder.StateFailed, "失败"
		case "interrupted", "cancelled":
			return qoder.StateInterrupted, "已中断"
		}
		return qoder.StateIdle, "空闲"
	case "tool/call":
		if fresh {
			if ev.Name != "" {
				return qoder.StateTool, "执行工具 · " + ev.Name
			}
			return qoder.StateTool, "执行工具"
		}
	case "assistant/message", "step/start", "step/end":
		if fresh {
			return qoder.StateThinking, "生成中"
		}
	case "llm/retry", "llm/retry-started":
		if fresh {
			return qoder.StateThinking, "重试中"
		}
	case "user/message":
		if fresh {
			return qoder.StateStarting, "启动中"
		}
	}
	return qoder.StateIdle, "空闲"
}

func (s *Source) title(ctx context.Context, db *sql.DB, sessionID string) string {
	const query = `
		select e.f_data
		from t_session_events se
		join t_events e on e.f_event_id = se.f_event_id
		where se.f_session_id = ? and e.f_kind = 'session/title'
		order by se.f_sequence desc limit 1`
	var raw string
	if err := db.QueryRowContext(ctx, query, sessionID).Scan(&raw); err != nil {
		return ""
	}
	var data struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal([]byte(raw), &data)
	return strings.TrimSpace(data.Title)
}

type eventRow struct {
	Kind string
	Data string
	At   time.Time
}

func (s *Source) metrics(ctx context.Context, db *sql.DB, sessionID string, live bool, now time.Time) qoder.Metrics {
	const query = `
		select e.f_kind, coalesce(e.f_data, ''), e.f_created_at
		from t_session_events se
		join t_events e on e.f_event_id = se.f_event_id
		where se.f_session_id = ? and e.f_kind in ('turn/start', 'turn/end', 'assistant/message')
		order by se.f_sequence desc limit 400`
	rows, err := db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return qoder.Metrics{}
	}
	defer rows.Close()

	type turn struct {
		start, end time.Time
		first      time.Time
	}
	turns := map[int]*turn{}
	var order []int
	for rows.Next() {
		var kind, raw string
		var at int64
		if err := rows.Scan(&kind, &raw, &at); err != nil {
			return qoder.Metrics{}
		}
		var data struct {
			Turn int `json:"turn"`
		}
		_ = json.Unmarshal([]byte(raw), &data)
		if data.Turn == 0 {
			continue
		}
		entry := turns[data.Turn]
		if entry == nil {
			entry = &turn{}
			turns[data.Turn] = entry
			order = append(order, data.Turn)
		}
		when := time.UnixMilli(at)
		switch kind {
		case "turn/start":
			if entry.start.IsZero() {
				entry.start = when
			}
		case "turn/end":
			if entry.end.IsZero() {
				entry.end = when
			}
		case "assistant/message":
			if entry.first.IsZero() || when.Before(entry.first) {
				entry.first = when
			}
		}
	}
	if err := rows.Err(); err != nil {
		return qoder.Metrics{}
	}

	var recentTurns []qoder.TurnMetric
	for i := 0; i < len(order) && len(recentTurns) < recent; i++ {
		entry := turns[order[i]]
		if entry.start.IsZero() {
			continue
		}
		metric := qoder.TurnMetric{TurnID: fmtTurn(order[i]), StartedAt: entry.start, ApproxTTFT: true}
		end := entry.end
		isLive := live && i == 0 && end.IsZero()
		if isLive {
			end = now
		}
		if !entry.first.IsZero() {
			metric.TTFT = entry.first.Sub(entry.start)
			metric.PostFirst = end.Sub(entry.first)
			metric.ApproxTTFT = false
		} else if isLive {
			metric.TTFT = now.Sub(entry.start)
		}
		metric.EndedAt = end
		recentTurns = append(recentTurns, metric)
	}

	metrics := qoder.Metrics{Recent: recentTurns}
	hub.ComputeAverages(&metrics)
	if live && len(recentTurns) > 0 {
		item := recentTurns[0]
		metrics.Live = &item
	}
	return metrics
}

func fmtTurn(turn int) string {
	return "turn-" + strconv.Itoa(turn)
}
