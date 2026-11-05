package qoder

import (
	"sort"
	"time"
)

type State string

const (
	StateNotRunning      State = "not-running"
	StateIdle            State = "idle"
	StateStarting        State = "starting"
	StateThinking        State = "thinking"
	StateTool            State = "tool"
	StateRunning         State = "running"
	StateWaitingInput    State = "waiting-input"
	StatePermission      State = "permission"
	StateQueued          State = "queued"
	StateReconnecting    State = "reconnecting"
	StateReconnectFailed State = "reconnect-failed"
	StateFailed          State = "failed"
	StateInterrupted     State = "interrupted"
	StateCompleted       State = "completed"
)

func (s State) Label() string {
	switch s {
	case StateNotRunning:
		return "未运行"
	case StateIdle:
		return "空闲"
	case StateStarting:
		return "启动中"
	case StateThinking:
		return "生成中"
	case StateTool:
		return "执行工具"
	case StateRunning:
		return "运行中"
	case StateWaitingInput:
		return "等待回复"
	case StatePermission:
		return "权限申请"
	case StateQueued:
		return "排队中"
	case StateReconnecting:
		return "重连中"
	case StateReconnectFailed:
		return "重连失败停止"
	case StateFailed:
		return "失败停止"
	case StateInterrupted:
		return "已中断"
	case StateCompleted:
		return "已完成"
	}
	return string(s)
}

func (s State) Priority() int {
	switch s {
	case StatePermission:
		return 100
	case StateWaitingInput:
		return 90
	case StateReconnectFailed:
		return 85
	case StateFailed:
		return 80
	case StateInterrupted:
		return 75
	case StateThinking, StateTool, StateRunning, StateStarting:
		return 70
	case StateQueued:
		return 60
	case StateReconnecting:
		return 50
	case StateCompleted:
		return 30
	case StateIdle:
		return 20
	}
	return 0
}

func (s State) Active() bool {
	switch s {
	case StateStarting, StateThinking, StateTool, StateRunning, StateWaitingInput,
		StatePermission, StateQueued, StateReconnecting:
		return true
	}
	return false
}

func (s State) NoticeKind() (NoticeKind, bool) {
	switch s {
	case StateCompleted:
		return NoticeCompleted, true
	case StateFailed:
		return NoticeFailed, true
	case StateReconnectFailed:
		return NoticeReconnectFailed, true
	case StateWaitingInput:
		return NoticeWaitingInput, true
	case StatePermission:
		return NoticePermission, true
	case StateInterrupted:
		return NoticeInterrupted, true
	}
	return "", false
}

type TurnMetric struct {
	TurnID       string
	TTFT         time.Duration
	PostFirst    time.Duration
	TokensPerSec float64
	OutputTokens int
	ApproxTTFT   bool
	StartedAt    time.Time
	EndedAt      time.Time
}

type Metrics struct {
	AvgTTFT      time.Duration
	AvgPostFirst time.Duration
	AvgTPS       float64
	Recent       []TurnMetric
	Live         *TurnMetric
}

type Session struct {
	AgentID      string
	AgentName    string
	ID           string
	Project      string
	Title        string
	Model        string
	Cwd          string
	State        State
	Detail       string
	Pending      int
	Queued       int
	ActiveTurnID string
	LastActivity time.Time
	Metrics      Metrics
	noticeAt     time.Time
}

type NoticeKind string

const (
	NoticeCompleted       NoticeKind = "completed"
	NoticeFailed          NoticeKind = "failed"
	NoticeReconnectFailed NoticeKind = "reconnect-failed"
	NoticeWaitingInput    NoticeKind = "waiting-input"
	NoticePermission      NoticeKind = "permission"
	NoticeInterrupted     NoticeKind = "interrupted"
)

func (k NoticeKind) Label() string {
	switch k {
	case NoticeCompleted:
		return "任务结束"
	case NoticeFailed:
		return "失败停止"
	case NoticeReconnectFailed:
		return "重连失败停止"
	case NoticeWaitingInput:
		return "等待回复"
	case NoticePermission:
		return "权限申请"
	case NoticeInterrupted:
		return "已中断"
	}
	return string(k)
}

type Notice struct {
	SessionID string     `json:"sessionId"`
	AgentID   string     `json:"agentId"`
	Project   string     `json:"project"`
	Title     string     `json:"title"`
	Kind      NoticeKind `json:"kind"`
	Since     time.Time  `json:"since"`
}

type Snapshot struct {
	QoderRunning bool      `json:"qoderRunning"`
	Disabled     bool      `json:"disabled,omitempty"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Sessions     []Session `json:"sessions"`
	FocusedID    string    `json:"focusedId"`
	Notices      []Notice  `json:"notices"`
	Error        string    `json:"error,omitempty"`
}

func SortSessions(sessions []Session) {
	sort.SliceStable(sessions, func(i, j int) bool {
		pi, pj := sessions[i].State.Priority(), sessions[j].State.Priority()
		if pi != pj {
			return pi > pj
		}
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})
}

func FocusSession(sessions []Session) string {
	best := ""
	for i := range sessions {
		s := &sessions[i]
		if s.ID == "" {
			continue
		}
		if best == "" {
			best = s.ID
			continue
		}
		var b *Session
		for j := range sessions {
			if sessions[j].ID == best {
				b = &sessions[j]
				break
			}
		}
		if b == nil {
			best = s.ID
			continue
		}
		if s.State.Priority() > b.State.Priority() ||
			(s.State.Priority() == b.State.Priority() && s.LastActivity.After(b.LastActivity)) {
			best = s.ID
		}
	}
	return best
}
