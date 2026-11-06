package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nono/internal/qoder"
)

const (
	AgentID   = "codex"
	AgentName = "Codex"

	sessionWindow = 30 * time.Minute
	staleLive     = 10 * time.Minute
	noticeLimit   = 10
	recentTurns   = 5
)

type Collector struct {
	mu          sync.Mutex
	roots       Roots
	threads     *store
	history     *store
	enabled     bool
	muteDone    bool
	skipRunning bool
	window      time.Duration
	prev        map[string]qoder.State
	queue       noticeQueue
	baseline    bool
	rollouts    map[string]*rolloutReader
	firstOutput map[string]int64
}

func NewCollector() *Collector {
	roots := Locate()
	c := &Collector{
		roots:       roots,
		enabled:     true,
		window:      sessionWindow,
		prev:        map[string]qoder.State{},
		rollouts:    map[string]*rolloutReader{},
		firstOutput: map[string]int64{},
	}
	c.openStores()
	return c
}

func (c *Collector) openStores() {
	if c.threads == nil && c.roots.StateDB != "" {
		if s, err := openStore(c.roots.StateDB); err == nil {
			c.threads = s
		}
	}
	if c.history == nil && c.roots.HistoryDB != "" {
		if s, err := openStore(c.roots.HistoryDB); err == nil {
			c.history = s
		}
	}
}

func (c *Collector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.threads != nil {
		c.threads.Close()
		c.threads = nil
	}
	if c.history != nil {
		c.history.Close()
		c.history = nil
	}
}

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

// SetEnabled turns Codex collection on or off without a restart.
func (c *Collector) SetEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enabled == enabled {
		return
	}
	c.enabled = enabled
	c.reset()
}

func (c *Collector) reset() {
	c.prev = map[string]qoder.State{}
	c.queue.clear()
	c.baseline = false
}

// View returns the Codex sessions in the shape the notch page consumes.
func (c *Collector) View(ctx context.Context) qoder.View {
	return qoder.SnapshotView(c.collect(ctx))
}

func (c *Collector) collect(ctx context.Context) qoder.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	snapshot := qoder.Snapshot{GeneratedAt: now}
	if !c.enabled {
		c.reset()
		snapshot.Disabled = true
		return snapshot
	}
	if !c.skipRunning && !CodexRunning() {
		c.reset()
		return snapshot
	}
	c.openStores()
	if c.threads == nil || c.history == nil {
		snapshot.Error = "Codex 数据库不可用"
		return snapshot
	}
	snapshot.QoderRunning = true

	window := c.window
	if window <= 0 {
		window = sessionWindow
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	threads, err := c.threads.Threads(qctx, now.Add(-window))
	if err != nil {
		snapshot.Error = err.Error()
	}

	sessions := make([]qoder.Session, 0, len(threads))
	entries := make([]noticeEntry, 0, len(threads))
	for _, thread := range threads {
		turns, err := c.history.Turns(qctx, thread.ID, recentTurns)
		if err != nil {
			continue
		}
		var stats *rolloutStats
		if reader := c.rolloutFor(thread.RolloutPath); reader != nil {
			reader.refresh()
			stats = &reader.stats
		}

		last := thread.UpdatedAt
		for _, turn := range turns {
			if turn.StartedAt.After(last) {
				last = turn.StartedAt
			}
			if turn.EndedAt.After(last) {
				last = turn.EndedAt
			}
		}
		if stats != nil && stats.LastEventAt.After(last) {
			last = stats.LastEventAt
		}

		state := qoder.StateIdle
		detail := "空闲"
		live := false
		if len(turns) > 0 {
			latest := turns[0]
			live = c.turnLive(latest, stats, last, now)
			state, detail = deriveState(latest, live, stats)
		}
		if !live && now.Sub(last) > window {
			continue
		}

		metrics := c.metrics(qctx, thread.ID, turns, stats, live, last, now)
		for _, turn := range metrics.Recent {
			if turn.EndedAt.After(last) {
				last = turn.EndedAt
			}
		}
		model := thread.Model
		if model == "" && stats != nil {
			model = stats.Model
		}
		project := projectName(thread.Cwd)
		title := thread.Title
		if title == "" {
			title = project
		}
		session := qoder.Session{
			AgentID:      AgentID,
			AgentName:    AgentName,
			ID:           thread.ID,
			Project:      project,
			Title:        title,
			Model:        shortModel(model),
			Cwd:          cleanPath(thread.Cwd),
			State:        state,
			Detail:       detail,
			LastActivity: last,
			Metrics:      metrics,
		}
		if live && len(turns) > 0 {
			session.ActiveTurnID = turns[0].ID
		}
		sessions = append(sessions, session)
		entries = append(entries, noticeEntry{session: session, since: last})
	}

	qoder.SortSessions(sessions)
	snapshot.Sessions = sessions
	snapshot.FocusedID = qoder.FocusSession(sessions)
	c.updateNotices(entries, now)
	snapshot.Notices = c.queue.snapshot()
	return snapshot
}

func (c *Collector) turnLive(turn Turn, stats *rolloutStats, last, now time.Time) bool {
	finished := false
	active := false
	if stats != nil && stats.LastTurn == turn.ID {
		switch stats.LastEvent {
		case "task_started":
			active = true
		case "task_complete", "turn_aborted":
			finished = true
		}
	}
	if finished {
		return false
	}
	if active {
		return now.Sub(last) <= staleLive
	}
	if turn.Status != "inProgress" {
		return false
	}
	return now.Sub(last) <= staleLive
}

func deriveState(turn Turn, live bool, stats *rolloutStats) (qoder.State, string) {
	if stats != nil && stats.LastTurn == turn.ID {
		switch stats.LastEvent {
		case "task_complete":
			return qoder.StateCompleted, "已完成"
		case "turn_aborted":
			return qoder.StateInterrupted, "已中断"
		}
	}
	if live {
		if stats != nil && stats.LastTurn == turn.ID && stats.LastEvent == "task_started" {
			switch stats.LastItemType {
			case "CommandExecution":
				if stats.LastItemDetail != "" {
					return qoder.StateTool, "执行命令 · " + stats.LastItemDetail
				}
				return qoder.StateTool, "执行命令"
			case "FileChange":
				return qoder.StateTool, "修改文件"
			case "McpToolCall":
				return qoder.StateTool, "调用工具"
			case "Reasoning", "AgentMessage":
				return qoder.StateThinking, "生成中"
			case "UserMessage":
				return qoder.StateStarting, "启动中"
			}
		}
		return qoder.StateRunning, "运行中"
	}
	switch turn.Status {
	case "failed":
		if msg := errorMessage(turn.Error); msg != "" {
			return qoder.StateFailed, "失败 · " + msg
		}
		return qoder.StateFailed, "失败"
	case "interrupted":
		return qoder.StateInterrupted, "已中断"
	case "completed":
		return qoder.StateCompleted, "已完成"
	}
	return qoder.StateIdle, "空闲"
}

func errorMessage(raw string) string {
	if raw == "" {
		return ""
	}
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed.Message != "" {
		raw = parsed.Message
	}
	raw = strings.Join(strings.Fields(raw), " ")
	if len(raw) > 80 {
		raw = raw[:80] + "…"
	}
	return raw
}

func (c *Collector) metrics(ctx context.Context, threadID string, turns []Turn, stats *rolloutStats, live bool, last, now time.Time) qoder.Metrics {
	recent := make([]qoder.TurnMetric, 0, len(turns))
	for i, turn := range turns {
		if turn.StartedAt.IsZero() {
			continue
		}
		isLive := live && i == 0
		var first time.Time
		if stats != nil {
			if tr := stats.Turns[turn.ID]; tr != nil {
				first = tr.FirstOutput
			}
		}
		if first.IsZero() {
			first = c.firstOutputTime(ctx, threadID, turn.ID, isLive)
		}
		end := turn.EndedAt
		switch {
		case isLive:
			end = now
		case end.IsZero():
			// A killed turn has no completion time; the thread's last activity
			// is the closest bound and avoids multi-day durations.
			if last.After(turn.StartedAt) {
				end = last
			} else {
				end = turn.StartedAt
			}
		}
		metric := qoder.TurnMetric{
			TurnID:     turn.ID,
			StartedAt:  turn.StartedAt,
			EndedAt:    end,
			ApproxTTFT: true,
		}
		if !first.IsZero() && !first.Before(turn.StartedAt) {
			metric.TTFT = first.Sub(turn.StartedAt)
			metric.PostFirst = end.Sub(first)
			metric.ApproxTTFT = false
		} else if isLive {
			metric.TTFT = now.Sub(turn.StartedAt)
		} else {
			metric.PostFirst = end.Sub(turn.StartedAt)
		}
		if stats != nil {
			if tr := stats.Turns[turn.ID]; tr != nil {
				metric.OutputTokens = tr.OutputTokens
				if tr.OutputTokens > 0 && tr.LastToken.After(tr.FirstToken) {
					metric.TokensPerSec = float64(tr.OutputTokens) / tr.LastToken.Sub(tr.FirstToken).Seconds()
				}
			}
		}
		recent = append(recent, metric)
	}
	metrics := qoder.Metrics{Recent: recent}
	computeAverages(&metrics)
	if live && len(recent) > 0 {
		item := recent[0]
		metrics.Live = &item
	}
	return metrics
}

func (c *Collector) firstOutputTime(ctx context.Context, threadID, turnID string, live bool) time.Time {
	key := threadID + "|" + turnID
	if ms, ok := c.firstOutput[key]; ok && (ms > 0 || !live) {
		if ms <= 0 {
			return time.Time{}
		}
		return time.UnixMilli(ms)
	}
	ms, err := c.history.FirstOutputMs(ctx, threadID, turnID)
	if err != nil {
		return time.Time{}
	}
	c.firstOutput[key] = ms
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func computeAverages(metrics *qoder.Metrics) {
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

func (c *Collector) rolloutFor(path string) *rolloutReader {
	path = cleanPath(path)
	if path == "" {
		return nil
	}
	reader := c.rollouts[path]
	if reader == nil {
		reader = newRolloutReader(path)
		c.rollouts[path] = reader
	}
	return reader
}

func cleanPath(path string) string {
	return strings.TrimPrefix(path, `\\?\`)
}

func projectName(cwd string) string {
	cwd = cleanPath(cwd)
	if cwd != "" {
		name := filepath.Base(filepath.Clean(cwd))
		if name != "." && name != string(filepath.Separator) && name != "" {
			return name
		}
	}
	return AgentName
}

func shortModel(model string) string {
	if i := strings.LastIndexAny(model, `/\`); i >= 0 && i+1 < len(model) {
		return model[i+1:]
	}
	return model
}

type noticeEntry struct {
	session qoder.Session
	since   time.Time
}

func (c *Collector) updateNotices(entries []noticeEntry, now time.Time) {
	if !c.baseline {
		for _, entry := range entries {
			c.prev[entry.session.ID] = entry.session.State
		}
		c.baseline = true
		return
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		session := entry.session
		present[session.ID] = true
		old, had := c.prev[session.ID]
		if kind, ok := session.State.NoticeKind(); ok {
			if !had || old != session.State {
				if kind == qoder.NoticeCompleted && c.muteDone {
					c.queue.remove(session.ID)
					c.prev[session.ID] = session.State
					continue
				}
				since := entry.since
				if since.IsZero() {
					since = now
				}
				c.queue.enqueue(qoder.Notice{
					SessionID: session.ID,
					AgentID:   AgentID,
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

// AckNotice removes a notice the user handled or skipped.
func (c *Collector) AckNotice(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue.remove(sessionID)
}

// Probe prints the collected Codex view, including recent history, without
// requiring Codex to be running. It backs `nono codex`.
func Probe() {
	c := NewCollector()
	defer c.Close()
	c.mu.Lock()
	c.skipRunning = true
	c.window = 7 * 24 * time.Hour
	c.mu.Unlock()
	view := c.View(context.Background())
	data, _ := json.MarshalIndent(view, "", "  ")
	fmt.Println(string(data))
}
