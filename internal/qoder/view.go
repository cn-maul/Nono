package qoder

import (
	"context"
	"time"
)

// View is the JSON shape the notch page consumes.
type View struct {
	QoderRunning bool          `json:"qoderRunning"`
	Disabled     bool          `json:"disabled,omitempty"`
	GeneratedAt  int64         `json:"generatedAt"`
	Error        string        `json:"error,omitempty"`
	FocusedID    string        `json:"focusedId,omitempty"`
	Sessions     []ViewSession `json:"sessions"`
	Notices      []ViewNotice  `json:"notices"`
}

type ViewSession struct {
	ID             string     `json:"id"`
	AgentID        string     `json:"agentId"`
	Agent          string     `json:"agent"`
	Project        string     `json:"project"`
	Title          string     `json:"title"`
	Model          string     `json:"model"`
	State          string     `json:"state"`
	Label          string     `json:"label"`
	Detail         string     `json:"detail,omitempty"`
	Pending        int        `json:"pending"`
	Queued         int        `json:"queued"`
	LastActivity   int64      `json:"lastActivity"`
	AvgTTFTms      int64      `json:"avgTtftMs"`
	AvgPostFirstMs int64      `json:"avgPostFirstMs"`
	AvgTPS         float64    `json:"avgTps"`
	Live           *ViewTurn  `json:"live,omitempty"`
	Recent         []ViewTurn `json:"recent"`
}

type ViewTurn struct {
	TurnID       string  `json:"turnId"`
	TTFTms       int64   `json:"ttftMs"`
	PostFirstMs  int64   `json:"postFirstMs"`
	TPS          float64 `json:"tps"`
	OutputTokens int     `json:"outputTokens"`
	ApproxTTFT   bool    `json:"approxTtft,omitempty"`
	StartedAt    int64   `json:"startedAt"`
	EndedAt      int64   `json:"endedAt"`
}

type ViewNotice struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Since     int64  `json:"since"`
}

func SnapshotView(snapshot Snapshot) View {
	out := View{
		QoderRunning: snapshot.QoderRunning,
		Disabled:     snapshot.Disabled,
		GeneratedAt:  unixMillis(snapshot.GeneratedAt),
		Error:        snapshot.Error,
		FocusedID:    snapshot.FocusedID,
		Sessions:     make([]ViewSession, 0, len(snapshot.Sessions)),
		Notices:      make([]ViewNotice, 0, len(snapshot.Notices)),
	}
	for _, session := range snapshot.Sessions {
		view := ViewSession{
			ID:             session.ID,
			AgentID:        session.AgentID,
			Agent:          session.AgentName,
			Project:        session.Project,
			Title:          session.Title,
			Model:          session.Model,
			State:          string(session.State),
			Label:          session.State.Label(),
			Detail:         session.Detail,
			Pending:        session.Pending,
			Queued:         session.Queued,
			LastActivity:   unixMillis(session.LastActivity),
			AvgTTFTms:      session.Metrics.AvgTTFT.Milliseconds(),
			AvgPostFirstMs: session.Metrics.AvgPostFirst.Milliseconds(),
			AvgTPS:         round1(session.Metrics.AvgTPS),
			Recent:         make([]ViewTurn, 0, len(session.Metrics.Recent)),
		}
		if session.Metrics.Live != nil {
			turn := viewTurn(*session.Metrics.Live)
			view.Live = &turn
		}
		for _, turn := range session.Metrics.Recent {
			view.Recent = append(view.Recent, viewTurn(turn))
		}
		out.Sessions = append(out.Sessions, view)
	}
	for _, notice := range snapshot.Notices {
		out.Notices = append(out.Notices, ViewNotice{
			SessionID: notice.SessionID,
			AgentID:   notice.AgentID,
			Project:   notice.Project,
			Title:     notice.Title,
			Kind:      string(notice.Kind),
			Label:     notice.Kind.Label(),
			Since:     unixMillis(notice.Since),
		})
	}
	return out
}

func viewTurn(turn TurnMetric) ViewTurn {
	return ViewTurn{
		TurnID:       turn.TurnID,
		TTFTms:       turn.TTFT.Milliseconds(),
		PostFirstMs:  turn.PostFirst.Milliseconds(),
		TPS:          round1(turn.TokensPerSec),
		OutputTokens: turn.OutputTokens,
		ApproxTTFT:   turn.ApproxTTFT,
		StartedAt:    unixMillis(turn.StartedAt),
		EndedAt:      unixMillis(turn.EndedAt),
	}
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (c *Collector) View(ctx context.Context) View {
	return SnapshotView(c.Collect(ctx))
}

// AckNotice removes a notice the user handled or skipped.
func (c *Collector) AckNotice(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue.remove(sessionID)
}
