package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nono/internal/hub"
	"nono/internal/qoder"
)

const (
	AgentID   = "opencode"
	AgentName = "OpenCode"

	window    = 30 * time.Minute
	staleLive = 2 * time.Minute
	recent    = 5

	defaultWindow = window
)

type Source struct {
	mu    sync.Mutex
	db    *sql.DB
	first map[string]int64
}

func New() *Source { return &Source{first: map[string]int64{}} }

func (s *Source) ID() string   { return AgentID }
func (s *Source) Name() string { return AgentName }

func (s *Source) Running() bool {
	if hub.RunningContaining("opencode") {
		return true
	}
	db, err := s.store()
	if err != nil {
		return false
	}
	var latest int64
	if err := db.QueryRow(`select coalesce(max(time_updated), 0) from session`).Scan(&latest); err != nil {
		return false
	}
	return time.Since(time.UnixMilli(latest)) <= window
}

func (s *Source) Focus() error {
	return hub.FocusContaining("opencode", "opencode")
}

func (s *Source) store() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	home, _ := os.UserHomeDir()
	db, err := hub.OpenReadOnly(filepath.Join(home, ".local", "share", "opencode", "opencode.db"))
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
	const query = `
		select id, coalesce(title, ''), coalesce(directory, ''), coalesce(model, ''),
		       time_updated, coalesce(tokens_output, 0)
		from session
		where time_archived is null
		order by time_updated desc
		limit 20`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	window := hub.Window(defaultWindow)
	type row struct {
		id, title, directory, model string
		updated                     int64
	}
	var rowsOut []row
	for rows.Next() {
		var item row
		var tokens int
		if err := rows.Scan(&item.id, &item.title, &item.directory, &item.model, &item.updated, &tokens); err != nil {
			return nil, err
		}
		rowsOut = append(rowsOut, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var out []qoder.Session
	for _, item := range rowsOut {
		id, title, directory, model := item.id, item.title, item.directory, item.model
		last := time.UnixMilli(item.updated)
		if now.Sub(last) > window {
			continue
		}
		state, detail, eventAt := s.state(ctx, db, id, last, now)
		completed := s.completedAt(ctx, db, id)
		if state == qoder.StateIdle && !completed.IsZero() && now.Sub(completed) <= window {
			state, detail = qoder.StateCompleted, "已完成"
			if completed.After(last) {
				last = completed
			}
		}
		if eventAt.After(last) {
			last = eventAt
		}
		metrics := s.metrics(ctx, db, id, state.Active(), now)
		if title == "" {
			title = hub.ProjectName(directory, AgentName)
		}
		out = append(out, qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           id,
			Project:      hub.ProjectName(directory, AgentName),
			Title:        title,
			Model:        cleanModel(model),
			Cwd:          directory,
			State:        state,
			Detail:       detail,
			LastActivity: last,
			Metrics:      metrics,
		})
	}
	return out, nil
}

type eventPayload struct {
	Time int64 `json:"time"`
	Part struct {
		Type string `json:"type"`
		Tool string `json:"tool"`
		Time struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
		State struct {
			Status string `json:"status"`
		} `json:"state"`
	} `json:"part"`
	Info struct {
		Role string `json:"role"`
		Time struct {
			Created   int64 `json:"created"`
			Completed int64 `json:"completed"`
		} `json:"time"`
	} `json:"info"`
}

func (s *Source) state(ctx context.Context, db *sql.DB, sessionID string, updated time.Time, now time.Time) (qoder.State, string, time.Time) {
	const query = `select type, coalesce(data, '') from event where aggregate_id = ? order by seq desc limit 1`
	var kind, raw string
	if err := db.QueryRowContext(ctx, query, sessionID).Scan(&kind, &raw); err != nil {
		return qoder.StateIdle, "空闲", time.Time{}
	}
	var payload eventPayload
	_ = json.Unmarshal([]byte(raw), &payload)
	at := updated
	if payload.Time > 0 {
		at = time.Unix(payload.Time, 0)
	}
	fresh := now.Sub(at) <= staleLive
	switch kind {
	case "message.part.updated.1":
		part := payload.Part
		if part.Type == "tool" && (part.State.Status == "pending" || part.State.Status == "running") && fresh {
			name := strings.ToLower(part.Tool)
			if strings.Contains(name, "question") || strings.Contains(name, "ask") {
				return qoder.StateWaitingInput, "等待你回复", at
			}
			if part.Tool != "" {
				return qoder.StateTool, "执行工具 · " + part.Tool, at
			}
			return qoder.StateTool, "执行工具", at
		}
		if (part.Type == "text" || part.Type == "reasoning") && part.Time.End == 0 && fresh {
			return qoder.StateThinking, "生成中", at
		}
	case "message.updated.1":
		if payload.Info.Role == "user" && fresh {
			return qoder.StateStarting, "启动中", at
		}
		if payload.Info.Role == "assistant" && payload.Info.Time.Completed > 0 {
			return qoder.StateIdle, "空闲", at
		}
	}
	return qoder.StateIdle, "空闲", at
}

func (s *Source) completedAt(ctx context.Context, db *sql.DB, sessionID string) time.Time {
	const query = `select coalesce(data, '') from message where session_id = ? order by time_updated desc limit 1`
	var raw string
	if err := db.QueryRowContext(ctx, query, sessionID).Scan(&raw); err != nil {
		return time.Time{}
	}
	var data struct {
		Role string `json:"role"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	}
	if json.Unmarshal([]byte(raw), &data) != nil || data.Role != "assistant" || data.Time.Completed <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(data.Time.Completed)
}

type assistantMessage struct {
	ID        string
	Created   time.Time
	Completed time.Time
	Output    int
}

func (s *Source) metrics(ctx context.Context, db *sql.DB, sessionID string, live bool, now time.Time) qoder.Metrics {
	const query = `select id, coalesce(data, '') from message where session_id = ? order by time_updated desc limit 12`
	rows, err := db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return qoder.Metrics{}
	}
	defer rows.Close()

	var messages []assistantMessage
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return qoder.Metrics{}
		}
		var data struct {
			Role   string `json:"role"`
			Tokens struct {
				Output int `json:"output"`
			} `json:"tokens"`
			Time struct {
				Created   int64 `json:"created"`
				Completed int64 `json:"completed"`
			} `json:"time"`
		}
		if json.Unmarshal([]byte(raw), &data) != nil || data.Role != "assistant" {
			continue
		}
		message := assistantMessage{ID: id, Output: data.Tokens.Output}
		if data.Time.Created > 0 {
			message.Created = time.UnixMilli(data.Time.Created)
		}
		if data.Time.Completed > 0 {
			message.Completed = time.UnixMilli(data.Time.Completed)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return qoder.Metrics{}
	}

	var turns []qoder.TurnMetric
	for i, message := range messages {
		if len(turns) >= recent {
			break
		}
		if message.Created.IsZero() {
			continue
		}
		first := s.firstPart(ctx, db, message.ID)
		end := message.Completed
		isLive := live && i == 0 && end.IsZero()
		if isLive {
			end = now
		}
		if end.IsZero() {
			continue
		}
		metric := qoder.TurnMetric{
			TurnID:     message.ID,
			StartedAt:  message.Created,
			EndedAt:    end,
			ApproxTTFT: true,
		}
		if !first.IsZero() && !first.Before(message.Created) {
			metric.TTFT = first.Sub(message.Created)
			metric.PostFirst = end.Sub(first)
			metric.ApproxTTFT = false
			if message.Output > 0 {
				if span := end.Sub(first); span > 0 {
					metric.TokensPerSec = float64(message.Output) / span.Seconds()
				}
			}
		} else if isLive {
			metric.TTFT = now.Sub(message.Created)
		} else {
			metric.PostFirst = end.Sub(message.Created)
		}
		metric.OutputTokens = message.Output
		turns = append(turns, metric)
	}

	metrics := qoder.Metrics{Recent: turns}
	hub.ComputeAverages(&metrics)
	if live && len(turns) > 0 {
		item := turns[0]
		metrics.Live = &item
	}
	return metrics
}

func (s *Source) firstPart(ctx context.Context, db *sql.DB, messageID string) time.Time {
	s.mu.Lock()
	if ms, ok := s.first[messageID]; ok {
		s.mu.Unlock()
		if ms <= 0 {
			return time.Time{}
		}
		return time.UnixMilli(ms)
	}
	s.mu.Unlock()

	const query = `select coalesce(data, '') from part where message_id = ?`
	rows, err := db.QueryContext(ctx, query, messageID)
	if err != nil {
		return time.Time{}
	}
	defer rows.Close()
	var earliest int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var part struct {
			Type string `json:"type"`
			Time struct {
				Start int64 `json:"start"`
			} `json:"time"`
		}
		if json.Unmarshal([]byte(raw), &part) != nil {
			continue
		}
		switch part.Type {
		case "text", "reasoning", "tool":
		default:
			continue
		}
		if part.Time.Start > 0 && (earliest == 0 || part.Time.Start < earliest) {
			earliest = part.Time.Start
		}
	}
	if earliest > 0 {
		s.mu.Lock()
		s.first[messageID] = earliest
		s.mu.Unlock()
		return time.UnixMilli(earliest)
	}
	return time.Time{}
}

func cleanModel(model string) string {
	if strings.HasPrefix(strings.TrimSpace(model), "{") {
		var parsed struct {
			ModelID string `json:"modelID"`
		}
		if json.Unmarshal([]byte(model), &parsed) == nil && parsed.ModelID != "" {
			return parsed.ModelID
		}
		return ""
	}
	return model
}
