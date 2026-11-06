package codex

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Thread struct {
	ID          string
	Title       string
	Cwd         string
	Model       string
	UpdatedAt   time.Time
	RolloutPath string
}

type Turn struct {
	ID        string
	Status    string
	StartedAt time.Time
	EndedAt   time.Time
	Error     string
}

type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(3000)"
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
	return &store{db: db}, nil
}

func (s *store) Close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

// Threads lists non-archived conversations updated since the given time.
func (s *store) Threads(ctx context.Context, since time.Time) ([]Thread, error) {
	const query = `
		select id, coalesce(title, ''), coalesce(cwd, ''), coalesce(model, ''),
		       coalesce(updated_at_ms, updated_at * 1000, 0), coalesce(rollout_path, '')
		from threads
		where archived = 0
		  and coalesce(updated_at_ms, updated_at * 1000, 0) >= ?
		order by coalesce(updated_at_ms, updated_at * 1000, 0) desc
		limit 30`
	rows, err := s.db.QueryContext(ctx, query, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		var thread Thread
		var updated int64
		if err := rows.Scan(&thread.ID, &thread.Title, &thread.Cwd, &thread.Model, &updated, &thread.RolloutPath); err != nil {
			return nil, err
		}
		if updated > 0 {
			thread.UpdatedAt = time.UnixMilli(updated)
		}
		out = append(out, thread)
	}
	return out, rows.Err()
}

// Turns returns the newest turns of a thread, newest first.
func (s *store) Turns(ctx context.Context, threadID string, limit int) ([]Turn, error) {
	const query = `
		select turn_id, coalesce(status, ''), coalesce(started_at, 0),
		       coalesce(completed_at, 0), coalesce(error_json, '')
		from thread_turns
		where thread_id = ?
		order by coalesce(started_at, 0) desc
		limit ?`
	rows, err := s.db.QueryContext(ctx, query, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var turn Turn
		var started, completed int64
		if err := rows.Scan(&turn.ID, &turn.Status, &started, &completed, &turn.Error); err != nil {
			return nil, err
		}
		if turn.ID == "" {
			continue
		}
		if started > 0 {
			turn.StartedAt = time.Unix(started, 0)
		}
		if completed > 0 {
			turn.EndedAt = time.Unix(completed, 0)
		}
		out = append(out, turn)
	}
	return out, rows.Err()
}

// FirstOutputMs returns when the model first produced reasoning or text in a
// turn, which is where time-to-first-token ends.
func (s *store) FirstOutputMs(ctx context.Context, threadID, turnID string) (int64, error) {
	const query = `
		select min(coalesce(started_at_ms, created_at_ms, 0))
		from thread_items
		where thread_id = ? and turn_id = ?
		  and item_type in ('reasoning', 'agentMessage')`
	var first sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query, threadID, turnID).Scan(&first); err != nil {
		return 0, err
	}
	if !first.Valid {
		return 0, nil
	}
	return first.Int64, nil
}
