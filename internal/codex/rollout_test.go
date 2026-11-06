package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nono/internal/qoder"
)

const testRollout = `{"timestamp":"2026-11-05T10:00:00.000Z","type":"session_meta","payload":{"id":"thread-1","cwd":"C:\\work\\demo"}}
{"timestamp":"2026-11-05T10:00:00.100Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"rg/DeepSeek-V4.1-Flash","cwd":"C:\\work\\demo"}}
{"timestamp":"2026-11-05T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":1793863201}}
{"timestamp":"2026-11-05T10:00:03.500Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-1","item":{"type":"Reasoning","id":"rs_1"},"started_at_ms":1793863203500,"completed_at_ms":1793863204000}}
{"timestamp":"2026-11-05T10:00:04.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":100}}}}
{"timestamp":"2026-11-05T10:00:05.000Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-1","item":{"type":"CommandExecution","id":"call_1","command":["C:\\Program Files\\PowerShell\\7\\pwsh.exe","-Command","dir"]},"started_at_ms":1793863204500,"completed_at_ms":1793863205000}}
{"timestamp":"2026-11-05T10:00:06.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":160}}}}
{"timestamp":"2026-11-05T10:00:07.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}
`

func TestRolloutParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(testRollout), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := newRolloutReader(path)
	reader.refresh()
	stats := &reader.stats

	if stats.LastEvent != "task_complete" || stats.LastTurn != "turn-1" {
		t.Fatalf("lifecycle = %s/%s", stats.LastEvent, stats.LastTurn)
	}
	if stats.Model != "rg/DeepSeek-V4.1-Flash" || stats.Cwd != `C:\work\demo` {
		t.Fatalf("meta = %+v", stats)
	}
	turn := stats.Turns["turn-1"]
	if turn == nil {
		t.Fatal("turn missing")
	}
	if got := turn.FirstOutput.UnixMilli(); got != 1793863203500 {
		t.Fatalf("first output = %d", got)
	}
	if turn.OutputTokens != 60 {
		t.Fatalf("output tokens = %d, want 60", turn.OutputTokens)
	}
	if got := turn.LastToken.Sub(turn.FirstToken); got != 2*time.Second {
		t.Fatalf("token span = %s", got)
	}
	if turn.LastItemType != "CommandExecution" || turn.LastItemDetail != "pwsh" {
		t.Fatalf("last item = %s/%s", turn.LastItemType, turn.LastItemDetail)
	}
}

func TestDeriveState(t *testing.T) {
	stats := newRolloutStats()
	stats.LastTurn = "turn-1"
	stats.LastEvent = "task_started"
	stats.LastItemType = "CommandExecution"
	stats.LastItemDetail = "pwsh"
	turn := Turn{ID: "turn-1", Status: "inProgress"}

	state, detail := deriveState(turn, true, &stats)
	if state != qoder.StateTool || detail != "执行命令 · pwsh" {
		t.Fatalf("live tool = %s/%s", state, detail)
	}

	stats.LastItemType = "Reasoning"
	if state, _ := deriveState(turn, true, &stats); state != qoder.StateThinking {
		t.Fatalf("live reasoning = %s", state)
	}

	stats.LastEvent = "task_complete"
	if state, _ := deriveState(turn, false, &stats); state != qoder.StateCompleted {
		t.Fatalf("completed = %s", state)
	}

	failed := Turn{ID: "turn-2", Status: "failed", Error: `{"message":"tool not supported"}`}
	if state, detail := deriveState(failed, false, nil); state != qoder.StateFailed || detail != "失败 · tool not supported" {
		t.Fatalf("failed = %s/%s", state, detail)
	}
}

func TestErrorAndPaths(t *testing.T) {
	if got := errorMessage(`{"message":"line one\nline two"}`); got != "line one line two" {
		t.Fatalf("error message = %q", got)
	}
	if got := projectName(`\\?\C:\Users\louis\Desktop\project\Nono`); got != "Nono" {
		t.Fatalf("project = %q", got)
	}
	if got := shortModel(`rg/DeepSeek-V4.1-Flash`); got != "DeepSeek-V4.1-Flash" {
		t.Fatalf("model = %q", got)
	}
}
