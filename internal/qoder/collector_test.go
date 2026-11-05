package qoder

import (
	"testing"
	"time"
)

func TestNoticeTransitions(t *testing.T) {
	c := &Collector{prev: map[string]State{}}
	now := time.Now()
	step := func(state State) {
		c.updateNotices([]Session{{
			ID: "s1", AgentID: AgentID, Project: "p", Title: "t", State: state,
		}}, now)
	}
	queue := func() []Notice { return c.queue.snapshot() }

	step(StateRunning)
	if got := queue(); len(got) != 0 {
		t.Fatalf("baseline queued %d notices", len(got))
	}

	step(StateCompleted)
	if got := queue(); len(got) != 1 || got[0].Kind != NoticeCompleted {
		t.Fatalf("completed notice = %+v", got)
	}

	step(StateCompleted)
	if got := queue(); len(got) != 1 {
		t.Fatalf("duplicate completed notice = %+v", got)
	}

	step(StatePermission)
	if got := queue(); len(got) != 1 || got[0].Kind != NoticePermission {
		t.Fatalf("permission update = %+v", got)
	}

	step(StateRunning)
	if got := queue(); len(got) != 0 {
		t.Fatalf("resolved notice remains: %+v", got)
	}
}

func TestNoticeFIFO(t *testing.T) {
	c := &Collector{prev: map[string]State{}}
	base := time.Unix(1000, 0)
	c.updateNotices([]Session{
		{ID: "a", State: StateRunning},
		{ID: "b", State: StateRunning},
	}, base)

	c.updateNotices([]Session{
		{ID: "a", State: StateCompleted, noticeAt: base.Add(2 * time.Second)},
		{ID: "b", State: StateCompleted, noticeAt: base.Add(time.Second)},
	}, base.Add(3*time.Second))
	got := c.queue.snapshot()
	if len(got) != 2 || got[0].SessionID != "b" || got[1].SessionID != "a" {
		t.Fatalf("queue order = %+v", got)
	}
}

func TestNoticeQueueCap(t *testing.T) {
	c := &Collector{prev: map[string]State{}}
	now := time.Now()
	var running, completed []Session
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		running = append(running, Session{ID: id, State: StateRunning})
		completed = append(completed, Session{ID: id, State: StateCompleted})
	}
	c.updateNotices(running, now)
	c.updateNotices(completed, now)
	if got := len(c.queue.snapshot()); got != noticeLimit {
		t.Fatalf("queue length = %d, want %d", got, noticeLimit)
	}
}

func TestTokenRateWindow(t *testing.T) {
	stats := newSegmentStats()
	start := time.Unix(1000, 0)
	stats.Requests["r1"] = start.Add(time.Second)
	stats.Responses["segment-turn"] = []segmentResponse{{
		RequestID: "r1", At: start.Add(3 * time.Second), OutputTokens: 100,
	}}
	stats.Requests["r2"] = start.Add(20 * time.Second)
	stats.Responses["old-turn"] = []segmentResponse{{
		RequestID: "r2", At: start.Add(22 * time.Second), OutputTokens: 999,
	}}
	tps, tokens := stats.TokenRateWindow(start, start.Add(4*time.Second))
	if tokens != 100 {
		t.Fatalf("tokens = %d, want 100", tokens)
	}
	if tps != 50 {
		t.Fatalf("tps = %f, want 50", tps)
	}
}

func TestDeriveState(t *testing.T) {
	tool := newSegmentStats()
	tool.LastEvent = "tool.requested"
	tool.LastToolName = "Bash"
	question := newSegmentStats()
	question.LastToolName = "AskUserQuestion"
	cases := []struct {
		name    string
		control ControlState
		seg     *SegmentStats
		want    State
	}{
		{"permission", ControlState{RuntimeState: "waiting-user", Pending: 1}, newSegmentStats(), StatePermission},
		{"question", ControlState{RuntimeState: "waiting-user", Pending: 1}, question, StateWaitingInput},
		{"waiting", ControlState{RuntimeState: "waiting-user"}, newSegmentStats(), StateWaitingInput},
		{"tool", ControlState{RuntimeState: "running", ActiveTurnState: "running"}, tool, StateTool},
		{"completed", ControlState{RuntimeState: "ready", TerminalStatus: "completed"}, newSegmentStats(), StateCompleted},
		{"interrupted", ControlState{RuntimeState: "ready", TerminalStatus: "interrupted"}, newSegmentStats(), StateInterrupted},
		{"idle", ControlState{RuntimeState: "ready"}, newSegmentStats(), StateIdle},
		{"reconnect", ControlState{RuntimeState: "error"}, newSegmentStats(), StateReconnectFailed},
	}
	for _, tc := range cases {
		if got, _ := deriveState(tc.control, true, tc.seg); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestProjectAndModel(t *testing.T) {
	if got := projectName(`C:\Users\louis\Desktop\project\Nono`, ""); got != "Nono" {
		t.Fatalf("project = %q", got)
	}
	if got := shortModel("byok:provider-123/DeepSeek-V4.1-Flash"); got != "DeepSeek-V4.1-Flash" {
		t.Fatalf("model = %q", got)
	}
}
