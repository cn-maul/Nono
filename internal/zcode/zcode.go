package zcode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nono/internal/hub"
	"nono/internal/qoder"
)

const (
	AgentID   = "zcode"
	AgentName = "ZCode"

	window    = 30 * time.Minute
	liveLimit = 3 * time.Minute

	defaultWindow = window
)

type Source struct {
	mu sync.Mutex
	db *sql.DB
}

func New() *Source { return &Source{} }

func (s *Source) ID() string   { return AgentID }
func (s *Source) Name() string { return AgentName }

func (s *Source) Running() bool { return hub.RunningContaining("zcode") }

func (s *Source) Focus() error {
	return hub.FocusContaining("zcode", "zcode")
}

func (s *Source) store() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	home, _ := os.UserHomeDir()
	db, err := hub.OpenReadOnly(filepath.Join(home, ".zcode", "v2", "tasks-index.sqlite"))
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
		select task_id, coalesce(title, ''), coalesce(workspace_path, ''),
		       coalesce(task_status, ''), coalesce(model, ''), updated_at
		from tasks
		where coalesce(archived, 0) = 0 and coalesce(deleted, 0) = 0
		order by updated_at desc
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
		var id, title, workspace, status, model string
		var updated int64
		if err := rows.Scan(&id, &title, &workspace, &status, &model, &updated); err != nil {
			return nil, err
		}
		last := time.UnixMilli(updated)
		state, detail := stateOf(status, now.Sub(last))
		if !state.Active() && now.Sub(last) > window {
			continue
		}
		if title == "" {
			title = hub.ProjectName(workspace, AgentName)
		}
		out = append(out, qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           id,
			Project:      hub.ProjectName(workspace, AgentName),
			Title:        title,
			Model:        hub.ShortModel(model),
			Cwd:          workspace,
			State:        state,
			Detail:       detail,
			LastActivity: last,
			Metrics:      qoder.Metrics{},
		})
	}
	return out, rows.Err()
}

func stateOf(status string, age time.Duration) (qoder.State, string) {
	switch strings.ToLower(status) {
	case "running", "working", "in_progress", "generating":
		return qoder.StateThinking, "生成中"
	case "queued", "pending", "starting":
		return qoder.StateStarting, "启动中"
	case "completed", "done":
		return qoder.StateCompleted, "已完成"
	case "error", "failed":
		return qoder.StateFailed, "失败"
	case "cancelled", "canceled", "interrupted", "stopped":
		return qoder.StateInterrupted, "已中断"
	}
	if age <= liveLimit {
		return qoder.StateThinking, "生成中"
	}
	return qoder.StateIdle, "空闲"
}
