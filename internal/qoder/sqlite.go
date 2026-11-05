package qoder

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type SessionRow struct {
	ID        string
	Title     string
	Cwd       string
	Model     string
	UpdatedAt time.Time
}

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

func (s *Store) Sessions(ctx context.Context, since time.Time) ([]SessionRow, error) {
	const query = `
		select session_id, title, cwd, coalesce(model, ''), updated_at
		from chat_sessions
		where session_kind = 'standard'
		  and archived = 0
		  and deleted_at is null
		  and updated_at >= ?
		order by updated_at desc
		limit 30`
	rows, err := s.db.QueryContext(ctx, query, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var row SessionRow
		var updated int64
		if err := rows.Scan(&row.ID, &row.Title, &row.Cwd, &row.Model, &updated); err != nil {
			return nil, err
		}
		row.UpdatedAt = time.UnixMilli(updated)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) Session(ctx context.Context, id string) (SessionRow, bool, error) {
	const query = `
		select session_id, title, cwd, coalesce(model, ''), updated_at
		from chat_sessions
		where session_id = ? and archived = 0 and deleted_at is null`
	var row SessionRow
	var updated int64
	err := s.db.QueryRowContext(ctx, query, id).Scan(&row.ID, &row.Title, &row.Cwd, &row.Model, &updated)
	if err == sql.ErrNoRows {
		return SessionRow{}, false, nil
	}
	if err != nil {
		return SessionRow{}, false, err
	}
	row.UpdatedAt = time.UnixMilli(updated)
	return row, true, nil
}

type payloadPart struct {
	Type        string `json:"type"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
	DurationMS  int64  `json:"durationMs"`
}

type messagePayload struct {
	CompletedAt string `json:"completedAt"`
	DurationMS  int64  `json:"durationMs"`
	TurnMetrics struct {
		DurationMS int64 `json:"durationMs"`
	} `json:"turnMetrics"`
	Parts []payloadPart `json:"parts"`
}

// TurnMetrics returns up to limit recent assistant turns for a session.
// Token speed is filled in by the collector from the segment logs.
func (s *Store) TurnMetrics(ctx context.Context, sessionID string, limit int) ([]TurnMetric, error) {
	const query = `
		select turn_id, turn_started_at, created_at, payload_json
		from chat_session_messages
		where session_id = ?
		  and role = 'assistant'
		  and turn_id is not null
		  and turn_id <> ''
		order by created_at desc
		limit 120`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type turn struct {
		id       string
		start    time.Time
		first    time.Time
		end      time.Time
		approx   bool
		hasStart bool
	}
	var order []string
	turns := map[string]*turn{}
	for rows.Next() {
		var turnID string
		var started sql.NullInt64
		var created int64
		var payloadJSON string
		if err := rows.Scan(&turnID, &started, &created, &payloadJSON); err != nil {
			return nil, err
		}
		t, ok := turns[turnID]
		if !ok {
			if len(order) >= limit {
				continue
			}
			t = &turn{id: turnID}
			if started.Valid {
				t.start = time.UnixMilli(started.Int64)
				t.hasStart = true
			}
			turns[turnID] = t
			order = append(order, turnID)
		}
		if !t.hasStart && started.Valid {
			t.start = time.UnixMilli(started.Int64)
			t.hasStart = true
		}
		createdAt := time.UnixMilli(created)
		if t.end.IsZero() || createdAt.After(t.end) {
			t.end = createdAt
		}
		var payload messagePayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			continue
		}
		if payload.CompletedAt != "" {
			if at, err := time.Parse(time.RFC3339Nano, payload.CompletedAt); err == nil && at.After(t.end) {
				t.end = at
			}
		}
		duration := payload.DurationMS
		if payload.TurnMetrics.DurationMS > duration {
			duration = payload.TurnMetrics.DurationMS
		}
		if duration > 0 {
			if end := t.start.Add(time.Duration(duration) * time.Millisecond); end.After(t.end) {
				t.end = end
			}
		}
		for _, part := range payload.Parts {
			if part.StartedAt == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, part.StartedAt)
			if err != nil {
				continue
			}
			if t.first.IsZero() || at.Before(t.first) {
				t.first = at
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]TurnMetric, 0, len(order))
	for _, id := range order {
		t := turns[id]
		if t.start.IsZero() {
			if t.first.IsZero() {
				continue
			}
			t.start = t.first
			t.approx = true
		}
		if t.end.IsZero() {
			t.end = t.start
		}
		metric := TurnMetric{
			TurnID:     t.id,
			StartedAt:  t.start,
			EndedAt:    t.end,
			ApproxTTFT: true,
		}
		if !t.first.IsZero() {
			first := t.first
			if first.Before(t.start) {
				first = t.start
			}
			metric.TTFT = first.Sub(t.start)
			metric.PostFirst = t.end.Sub(first)
			metric.ApproxTTFT = false
		} else {
			metric.PostFirst = t.end.Sub(t.start)
		}
		out = append(out, metric)
	}
	return out, nil
}
