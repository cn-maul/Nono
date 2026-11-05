package qoder

func deriveState(control ControlState, hasControl bool, seg *SegmentStats) (State, string) {
	if !hasControl {
		return StateIdle, ""
	}
	switch control.RuntimeState {
	case "waiting-user":
		if control.Pending > 0 {
			if seg.PendingIsQuestion() {
				return StateWaitingInput, "等待你回复"
			}
			if seg.LastToolName != "" {
				return StatePermission, "等待权限确认 · " + seg.LastToolName
			}
			return StatePermission, "等待权限确认"
		}
		return StateWaitingInput, "等待你回复"
	case "error":
		return StateReconnectFailed, "连接失败"
	case "interrupted":
		return StateInterrupted, "已中断"
	case "opening":
		return StateStarting, "启动中"
	case "running":
		if control.ActiveTurnState == "failed" {
			return StateFailed, "失败"
		}
		if control.ActiveTurnState == "interrupted" {
			return StateInterrupted, "已中断"
		}
		if control.ActiveTurnState == "completed" {
			return StateCompleted, "已完成"
		}
		if control.Queued > 0 {
			return StateQueued, "排队中"
		}
		if control.ActiveTurnState == "starting" {
			return StateStarting, "启动中"
		}
		switch seg.LastEvent {
		case "tool.requested", "tool.shell.started", "tool.execution.finished":
			if seg.LastToolName != "" {
				return StateTool, "执行工具 · " + seg.LastToolName
			}
			return StateTool, "执行工具"
		case "model.request.started", "model.response.completed":
			return StateThinking, "生成中"
		}
		return StateRunning, "运行中"
	case "ready", "cold":
		if control.Queued > 0 {
			return StateQueued, "排队中"
		}
		switch control.TerminalStatus {
		case "completed":
			return StateCompleted, "已完成"
		case "failed":
			return StateFailed, "失败"
		case "interrupted":
			return StateInterrupted, "已中断"
		}
		return StateIdle, "空闲"
	}
	if control.ActiveTurnState == "running" || control.ActiveTurnState == "starting" {
		return StateRunning, "运行中"
	}
	return StateIdle, "空闲"
}
