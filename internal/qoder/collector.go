package qoder

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	AgentID   = "qoder"
	AgentName = "Qoder"

	sessionWindow = 30 * time.Minute
	activeWindow  = 2 * time.Hour
	noticeLimit   = 10
	recentTurns   = 5
)

type Collector struct {
	mu           sync.Mutex
	roots        Roots
	store        *Store
	logs         *LogWatcher
	segments     *SegmentReader
	prev         map[string]State
	baseline     bool
	queue        noticeQueue
	qoderEnabled bool
}

func NewCollector() *Collector {
	roots := Locate()
	c := &Collector{
		roots:        roots,
		logs:         NewLogWatcher(roots),
		segments:     NewSegmentReader(roots),
		prev:         map[string]State{},
		qoderEnabled: true,
	}
	if roots.DBPath != "" {
		if store, err := OpenStore(roots.DBPath); err == nil {
			c.store = store
		}
	}
	return c
}

func (c *Collector) Close() {
	if c.store != nil {
		c.store.Close()
	}
}

// SetQoderEnabled turns Qoder collection on or off without a restart.
func (c *Collector) SetQoderEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.qoderEnabled == enabled {
		return
	}
	c.qoderEnabled = enabled
	c.prev = map[string]State{}
	c.queue.clear()
	c.baseline = false
}

func (c *Collector) Collect(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if !c.qoderEnabled {
		c.prev = map[string]State{}
		c.queue.clear()
		c.baseline = false
		return Snapshot{GeneratedAt: now, Disabled: true}
	}
	snapshot := Snapshot{GeneratedAt: now}
	roots := Locate()
	if roots.DBPath != c.roots.DBPath || roots.UserDir != c.roots.UserDir {
		if c.store != nil {
			c.store.Close()
			c.store = nil
		}
		if roots.DBPath != "" {
			if store, err := OpenStore(roots.DBPath); err == nil {
				c.store = store
			} else {
				snapshot.Error = err.Error()
			}
		}
		c.roots = roots
		c.logs = NewLogWatcher(roots)
		c.segments = NewSegmentReader(roots)
		c.prev = map[string]State{}
		c.baseline = false
	}
	c.logs.Poll(roots)

	if !QoderRunning() {
		c.prev = map[string]State{}
		c.queue.clear()
		c.baseline = false
		return snapshot
	}
	snapshot.QoderRunning = true
	if c.store == nil {
		snapshot.Error = "Qoder 数据库不可用"
		return snapshot
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := c.store.Sessions(ctx, now.Add(-sessionWindow))
	if err != nil {
		snapshot.Error = err.Error()
	}

	have := make(map[string]bool, len(rows))
	for _, row := range rows {
		have[row.ID] = true
	}
	for id, control := range c.logs.States() {
		if have[id] || control.At.IsZero() || now.Sub(control.At) > activeWindow {
			continue
		}
		switch control.RuntimeState {
		case "running", "waiting-user", "opening", "error":
		default:
			continue
		}
		row, ok, err := c.store.Session(ctx, id)
		if err == nil && ok {
			rows = append(rows, row)
			have[id] = true
		}
	}

	sessions := make([]Session, 0, len(rows))
	for _, row := range rows {
		control, hasControl := c.logs.State(row.ID)
		seg := c.segments.Stats(row.ID)
		state, detail := deriveState(control, hasControl, seg)
		if c.logs.Reconnecting() && state == StateIdle {
			state, detail = StateReconnecting, "连接重试中"
		}
		last := row.UpdatedAt
		if hasControl && control.At.After(last) {
			last = control.At
		}
		if seg.LastEventAt.After(last) {
			last = seg.LastEventAt
		}
		noticeAt := last
		metrics := buildMetrics(ctx, c.store, row.ID, state, control, seg, recentTurns)
		for _, turn := range metrics.Recent {
			if turn.EndedAt.After(last) {
				last = turn.EndedAt
			}
		}
		if !state.Active() && now.Sub(last) > sessionWindow {
			continue
		}
		sessions = append(sessions, Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           row.ID,
			Project:      projectName(row.Cwd, row.Title),
			Title:        row.Title,
			Model:        shortModel(row.Model),
			Cwd:          row.Cwd,
			State:        state,
			Detail:       detail,
			Pending:      control.Pending,
			Queued:       control.Queued,
			ActiveTurnID: control.ActiveTurnID,
			LastActivity: last,
			Metrics:      metrics,
			noticeAt:     noticeAt,
		})
	}

	SortSessions(sessions)
	snapshot.Sessions = sessions
	snapshot.FocusedID = FocusSession(sessions)
	c.updateNotices(sessions, now)
	snapshot.Notices = c.queue.snapshot()
	return snapshot
}

func (c *Collector) updateNotices(sessions []Session, now time.Time) {
	if !c.baseline {
		for _, session := range sessions {
			c.prev[session.ID] = session.State
		}
		c.baseline = true
		return
	}
	present := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		present[session.ID] = true
		old, had := c.prev[session.ID]
		if kind, ok := session.State.NoticeKind(); ok {
			if !had || old != session.State {
				since := session.noticeAt
				if since.IsZero() {
					since = now
				}
				c.queue.enqueue(Notice{
					SessionID: session.ID,
					AgentID:   session.AgentID,
					Project:   session.Project,
					Title:     session.Title,
					Kind:      kind,
					Since:     since,
				})
			}
		} else if had && old != session.State {
			c.queue.remove(session.ID)
		}
		c.prev[session.ID] = session.State
	}
	for id := range c.prev {
		if !present[id] {
			delete(c.prev, id)
		}
	}
	for _, notice := range c.queue.snapshot() {
		if !present[notice.SessionID] {
			c.queue.remove(notice.SessionID)
		}
	}
}

func projectName(cwd, title string) string {
	if cwd != "" {
		name := filepath.Base(filepath.Clean(cwd))
		if name != "." && name != string(filepath.Separator) && name != "" {
			return name
		}
	}
	if title != "" {
		return title
	}
	return AgentName
}

func shortModel(model string) string {
	if i := strings.LastIndexAny(model, `/\`); i >= 0 && i+1 < len(model) {
		return model[i+1:]
	}
	return model
}

type noticeQueue struct {
	items []Notice
	index map[string]int
}

func (q *noticeQueue) enqueue(notice Notice) {
	if q.index == nil {
		q.index = map[string]int{}
	}
	if i, ok := q.index[notice.SessionID]; ok {
		q.items[i].Kind = notice.Kind
		q.items[i].Project = notice.Project
		q.items[i].Title = notice.Title
		return
	}
	if len(q.items) >= noticeLimit {
		q.remove(q.items[0].SessionID)
	}
	at := len(q.items)
	for i, item := range q.items {
		if notice.Since.Before(item.Since) {
			at = i
			break
		}
	}
	q.items = append(q.items, Notice{})
	copy(q.items[at+1:], q.items[at:])
	q.items[at] = notice
	for i := at; i < len(q.items); i++ {
		q.index[q.items[i].SessionID] = i
	}
}

func (q *noticeQueue) remove(sessionID string) {
	i, ok := q.index[sessionID]
	if !ok {
		return
	}
	q.items = append(q.items[:i], q.items[i+1:]...)
	delete(q.index, sessionID)
	for j := i; j < len(q.items); j++ {
		q.index[q.items[j].SessionID] = j
	}
}

func (q *noticeQueue) snapshot() []Notice {
	return append([]Notice(nil), q.items...)
}

func (q *noticeQueue) clear() {
	q.items = nil
	q.index = nil
}
