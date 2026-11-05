package qoder

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func RunProbe(args []string) {
	flags := flag.NewFlagSet("probe", flag.ExitOnError)
	watch := flags.Bool("watch", false, "keep printing when the snapshot changes")
	interval := flags.Duration("interval", time.Second, "poll interval in watch mode")
	if err := flags.Parse(args); err != nil {
		return
	}

	collector := NewCollector()
	defer collector.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	snapshot := collector.Collect(ctx)
	printProbe(snapshot, true)
	if !*watch {
		return
	}

	last := probeFingerprint(snapshot)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot = collector.Collect(ctx)
			current := probeFingerprint(snapshot)
			if current == last {
				continue
			}
			last = current
			printProbe(snapshot, false)
		}
	}
}

type probeSnapshot struct {
	QoderRunning bool           `json:"qoderRunning"`
	GeneratedAt  string         `json:"generatedAt"`
	FocusedID    string         `json:"focusedId,omitempty"`
	Error        string         `json:"error,omitempty"`
	Sessions     []probeSession `json:"sessions"`
	Notices      []probeNotice  `json:"notices"`
}

type probeSession struct {
	ID             string      `json:"id"`
	Agent          string      `json:"agent"`
	Project        string      `json:"project"`
	Title          string      `json:"title"`
	Model          string      `json:"model"`
	State          string      `json:"state"`
	Label          string      `json:"label"`
	Detail         string      `json:"detail,omitempty"`
	Pending        int         `json:"pending"`
	Queued         int         `json:"queued"`
	LastActivity   string      `json:"lastActivity,omitempty"`
	AvgTTFTms      int64       `json:"avgTtftMs"`
	AvgPostFirstMs int64       `json:"avgPostFirstMs"`
	AvgTPS         float64     `json:"avgTps"`
	Live           *probeTurn  `json:"live,omitempty"`
	Recent         []probeTurn `json:"recent"`
}

type probeTurn struct {
	TurnID       string  `json:"turnId"`
	TTFTms       int64   `json:"ttftMs"`
	PostFirstMs  int64   `json:"postFirstMs"`
	TPS          float64 `json:"tps"`
	OutputTokens int     `json:"outputTokens"`
	ApproxTTFT   bool    `json:"approxTtft,omitempty"`
	StartedAt    string  `json:"startedAt,omitempty"`
	EndedAt      string  `json:"endedAt,omitempty"`
}

type probeNotice struct {
	SessionID string `json:"sessionId"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Since     string `json:"since"`
}

func printProbe(snapshot Snapshot, pretty bool) {
	view := makeProbeSnapshot(snapshot)
	var data []byte
	if pretty {
		data, _ = json.MarshalIndent(view, "", "  ")
	} else {
		data, _ = json.Marshal(view)
	}
	fmt.Println(string(data))
}

func probeFingerprint(snapshot Snapshot) string {
	snapshot.GeneratedAt = time.Time{}
	data, _ := json.Marshal(makeProbeSnapshot(snapshot))
	return string(data)
}

func makeProbeSnapshot(snapshot Snapshot) probeSnapshot {
	out := probeSnapshot{
		QoderRunning: snapshot.QoderRunning,
		GeneratedAt:  formatTime(snapshot.GeneratedAt),
		FocusedID:    snapshot.FocusedID,
		Error:        snapshot.Error,
		Sessions:     make([]probeSession, 0, len(snapshot.Sessions)),
		Notices:      make([]probeNotice, 0, len(snapshot.Notices)),
	}
	for _, session := range snapshot.Sessions {
		view := probeSession{
			ID:             session.ID,
			Agent:          session.AgentName,
			Project:        session.Project,
			Title:          session.Title,
			Model:          session.Model,
			State:          string(session.State),
			Label:          session.State.Label(),
			Detail:         session.Detail,
			Pending:        session.Pending,
			Queued:         session.Queued,
			LastActivity:   formatTime(session.LastActivity),
			AvgTTFTms:      session.Metrics.AvgTTFT.Milliseconds(),
			AvgPostFirstMs: session.Metrics.AvgPostFirst.Milliseconds(),
			AvgTPS:         round1(session.Metrics.AvgTPS),
			Recent:         make([]probeTurn, 0, len(session.Metrics.Recent)),
		}
		if session.Metrics.Live != nil {
			turn := probeTurnOf(*session.Metrics.Live)
			view.Live = &turn
		}
		for _, turn := range session.Metrics.Recent {
			view.Recent = append(view.Recent, probeTurnOf(turn))
		}
		out.Sessions = append(out.Sessions, view)
	}
	for _, notice := range snapshot.Notices {
		out.Notices = append(out.Notices, probeNotice{
			SessionID: notice.SessionID,
			Agent:     AgentName,
			Project:   notice.Project,
			Title:     notice.Title,
			Kind:      string(notice.Kind),
			Label:     notice.Kind.Label(),
			Since:     formatTime(notice.Since),
		})
	}
	return out
}

func probeTurnOf(turn TurnMetric) probeTurn {
	return probeTurn{
		TurnID:       turn.TurnID,
		TTFTms:       turn.TTFT.Milliseconds(),
		PostFirstMs:  turn.PostFirst.Milliseconds(),
		TPS:          round1(turn.TokensPerSec),
		OutputTokens: turn.OutputTokens,
		ApproxTTFT:   turn.ApproxTTFT,
		StartedAt:    formatTime(turn.StartedAt),
		EndedAt:      formatTime(turn.EndedAt),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
