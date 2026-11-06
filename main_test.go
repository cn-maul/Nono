package main

import (
	"testing"

	"nono/internal/qoder"
)

func TestMergeViewsPrefersRunningSession(t *testing.T) {
	disabled := qoder.View{Disabled: true}
	completed := qoder.View{
		QoderRunning: true,
		Sessions: []qoder.ViewSession{{
			ID: "zcode-1", AgentID: "zcode", State: string(qoder.StateCompleted),
		}},
		FocusedID: "zcode-1",
	}
	running := qoder.View{
		QoderRunning: true,
		Sessions: []qoder.ViewSession{{
			ID: "workbuddy-1", AgentID: "workbuddy", State: string(qoder.StateThinking),
		}},
		FocusedID: "workbuddy-1",
	}

	merged := mergeViews([]qoder.View{disabled, {}, completed, running})
	if merged.FocusedID != "workbuddy-1" {
		t.Fatalf("focused = %q, want workbuddy-1", merged.FocusedID)
	}
	if merged.Disabled {
		t.Fatal("merged view should not be disabled")
	}
}

func TestMergeViewsNoticesWin(t *testing.T) {
	withNotice := qoder.View{
		QoderRunning: true,
		Sessions: []qoder.ViewSession{{
			ID: "codex-1", AgentID: "codex", State: string(qoder.StateCompleted),
		}},
		Notices: []qoder.ViewNotice{{SessionID: "codex-1", Kind: "completed", Since: 10}},
	}
	other := qoder.View{
		QoderRunning: true,
		Sessions: []qoder.ViewSession{{
			ID: "workbuddy-1", AgentID: "workbuddy", State: string(qoder.StateThinking),
		}},
	}
	merged := mergeViews([]qoder.View{other, withNotice})
	if merged.FocusedID != "codex-1" {
		t.Fatalf("focused = %q, want codex-1", merged.FocusedID)
	}
}
