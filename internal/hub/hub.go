package hub

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nono/internal/qoder"
)

const noticeLimit = 10

// Window returns def unless NONO_WINDOW_MINUTES overrides it, which the
// `nono agents` diagnostics use to inspect older sessions.
func Window(def time.Duration) time.Duration {
	if value := os.Getenv("NONO_WINDOW_MINUTES"); value != "" {
		if minutes, err := strconv.Atoi(value); err == nil && minutes > 0 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return def
}

// ProjectName picks the last path element, ignoring the \\?\ prefix Codex
// and other tools sometimes use.
func ProjectName(cwd, fallback string) string {
	cwd = strings.TrimPrefix(cwd, `\\?\`)
	if cwd != "" {
		name := filepath.Base(filepath.Clean(cwd))
		if name != "." && name != string(filepath.Separator) && name != "" {
			return name
		}
	}
	return fallback
}

func ShortModel(model string) string {
	if i := strings.LastIndexAny(model, `/\`); i >= 0 && i+1 < len(model) {
		return model[i+1:]
	}
	return model
}

// Source is one agent's local session store.
type Source interface {
	ID() string
	Name() string
	Running() bool
	Focus() error
	Sessions(ctx context.Context) ([]qoder.Session, error)
}

// Collector turns a Source into the shared view shape, adding sorting,
// notice transitions and enable/disable handling.
type Collector struct {
	mu       sync.Mutex
	source   Source
	enabled  bool
	muteDone bool
	prev     map[string]qoder.State
	queue    noticeQueue
	baseline bool
}

func New(source Source, enabled bool) *Collector {
	return &Collector{
		source:  source,
		enabled: enabled,
		prev:    map[string]qoder.State{},
	}
}

func (c *Collector) ID() string    { return c.source.ID() }
func (c *Collector) Name() string  { return c.source.Name() }
func (c *Collector) Enabled() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.enabled }
func (c *Collector) SetEnabled(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enabled == v {
		return
	}
	c.enabled = v
	c.reset()
}

func (c *Collector) Focus() error { return c.source.Focus() }

// SetNotifyCompleted controls whether task-finished states enter the queue.
func (c *Collector) SetNotifyCompleted(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.muteDone = !enabled
	if c.muteDone {
		for _, notice := range c.queue.snapshot() {
			if notice.Kind == qoder.NoticeCompleted {
				c.queue.remove(notice.SessionID)
			}
		}
	}
}

func (c *Collector) AckNotice(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue.remove(sessionID)
}

func (c *Collector) View(ctx context.Context) qoder.View {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	snapshot := qoder.Snapshot{GeneratedAt: now}
	if !c.enabled {
		c.reset()
		snapshot.Disabled = true
		return qoder.SnapshotView(snapshot)
	}
	if !c.source.Running() {
		c.reset()
		return qoder.SnapshotView(snapshot)
	}
	snapshot.QoderRunning = true

	sessions, err := c.source.Sessions(ctx)
	if err != nil {
		snapshot.Error = err.Error()
	}
	qoder.SortSessions(sessions)
	snapshot.Sessions = sessions
	snapshot.FocusedID = qoder.FocusSession(sessions)
	c.updateNotices(sessions, now)
	snapshot.Notices = c.queue.snapshot()
	return qoder.SnapshotView(snapshot)
}

func (c *Collector) reset() {
	c.prev = map[string]qoder.State{}
	c.queue.clear()
	c.baseline = false
}

func (c *Collector) updateNotices(sessions []qoder.Session, now time.Time) {
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
				if kind == qoder.NoticeCompleted && c.muteDone {
					c.queue.remove(session.ID)
					c.prev[session.ID] = session.State
					continue
				}
				since := session.LastActivity
				if since.IsZero() {
					since = now
				}
				c.queue.enqueue(qoder.Notice{
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

// ComputeAverages fills the averages of the recent turns.
func ComputeAverages(metrics *qoder.Metrics) {
	var ttftSum, postSum time.Duration
	var ttftCount, postCount, tpsCount int
	var tpsSum float64
	for _, turn := range metrics.Recent {
		if turn.TTFT > 0 && !turn.ApproxTTFT {
			ttftSum += turn.TTFT
			ttftCount++
		}
		if turn.PostFirst > 0 {
			postSum += turn.PostFirst
			postCount++
		}
		if turn.TokensPerSec > 0 {
			tpsSum += turn.TokensPerSec
			tpsCount++
		}
	}
	if ttftCount > 0 {
		metrics.AvgTTFT = ttftSum / time.Duration(ttftCount)
	}
	if postCount > 0 {
		metrics.AvgPostFirst = postSum / time.Duration(postCount)
	}
	if tpsCount > 0 {
		metrics.AvgTPS = tpsSum / float64(tpsCount)
	}
}

type noticeQueue struct {
	items []qoder.Notice
	index map[string]int
}

func (q *noticeQueue) enqueue(notice qoder.Notice) {
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
	q.items = append(q.items, qoder.Notice{})
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

func (q *noticeQueue) snapshot() []qoder.Notice {
	return append([]qoder.Notice(nil), q.items...)
}

func (q *noticeQueue) clear() {
	q.items = nil
	q.index = nil
}
