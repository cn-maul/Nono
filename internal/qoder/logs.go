package qoder

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ControlState struct {
	SessionID       string
	RuntimeState    string
	ActiveTurnID    string
	ActiveTurnState string
	Pending         int
	Queued          int
	TerminalStatus  string
	At              time.Time
}

type controlJSON struct {
	SessionID               string       `json:"sessionId"`
	RuntimeState            string       `json:"runtimeState"`
	ActiveTurnID            string       `json:"activeTurnId"`
	ActiveTurnState         string       `json:"activeTurnState"`
	PendingInteractionCount int          `json:"pendingInteractionCount"`
	QueuedTurnCount         int          `json:"queuedTurnCount"`
	TerminalStatus          string       `json:"terminalStatus"`
	Detail                  *controlJSON `json:"detail"`
}

func parseControlLine(line string) (ControlState, bool) {
	if !strings.Contains(line, "ChatSession control state") {
		return ControlState{}, false
	}
	start := strings.Index(line, "{")
	if start < 0 {
		return ControlState{}, false
	}
	var raw controlJSON
	if err := json.Unmarshal([]byte(line[start:]), &raw); err != nil {
		return ControlState{}, false
	}
	if raw.Detail != nil {
		raw = *raw.Detail
	}
	if raw.SessionID == "" {
		return ControlState{}, false
	}
	return ControlState{
		SessionID:       raw.SessionID,
		RuntimeState:    raw.RuntimeState,
		ActiveTurnID:    raw.ActiveTurnID,
		ActiveTurnState: raw.ActiveTurnState,
		Pending:         raw.PendingInteractionCount,
		Queued:          raw.QueuedTurnCount,
		TerminalStatus:  raw.TerminalStatus,
		At:              logTime(line),
	}, true
}

func logTime(line string) time.Time {
	open := strings.IndexByte(line, '[')
	close := strings.IndexByte(line, ']')
	if open >= 0 && close > open {
		if t, err := time.Parse(time.RFC3339Nano, line[open+1:close]); err == nil {
			return t
		}
	}
	return time.Now()
}

type lineTailer struct {
	path    string
	offset  int64
	partial []byte
}

func (t *lineTailer) toEnd() {
	st, err := os.Stat(t.path)
	if err != nil {
		return
	}
	t.offset = st.Size()
	t.partial = nil
}

func (t *lineTailer) lines() ([]string, error) {
	f, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < t.offset {
		t.offset = 0
		t.partial = nil
	}
	if st.Size() == t.offset {
		return nil, nil
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, st.Size()-t.offset)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]
	t.offset += int64(n)
	data := append(t.partial, buf...)
	parts := strings.Split(string(data), "\n")
	t.partial = []byte(parts[len(parts)-1])
	return parts[:len(parts)-1], nil
}

func tailLines(path string, max int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > max {
		if _, err := f.Seek(st.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	text := string(data)
	if st.Size() > max {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return strings.Split(strings.TrimRight(text, "\r\n"), "\n"), nil
}

type LogWatcher struct {
	mu           sync.Mutex
	runDir       string
	mainLog      *lineTailer
	renderer     *lineTailer
	states       map[string]ControlState
	reconnecting bool
}

func parseReconnectLine(line string) (bool, bool) {
	switch {
	case strings.Contains(line, "QCS SSE reconnect scheduled"):
		return true, true
	case strings.Contains(line, "QCS SSE reconnect failed"):
		return true, true
	case strings.Contains(line, "QCS SSE connected"):
		return false, true
	}
	return false, false
}

func NewLogWatcher(roots Roots) *LogWatcher {
	w := &LogWatcher{}
	w.rebuild(roots)
	return w
}

func (w *LogWatcher) rebuild(roots Roots) {
	w.runDir = roots.RunLogDir
	w.states = map[string]ControlState{}
	w.reconnecting = false
	w.mainLog = nil
	w.renderer = nil
	if w.runDir == "" {
		return
	}
	mainPath := filepath.Join(w.runDir, "main.log")
	rendererPath := filepath.Join(w.runDir, "renderer.log")
	for _, path := range []string{mainPath, rendererPath} {
		if lines, err := tailLines(path, 2<<20); err == nil {
			for _, line := range lines {
				if state, ok := parseControlLine(line); ok {
					w.states[state.SessionID] = state
				}
				if reconnect, ok := parseReconnectLine(line); ok {
					w.reconnecting = reconnect
				}
			}
		}
	}
	if exists(mainPath) {
		w.mainLog = &lineTailer{path: mainPath}
		w.mainLog.toEnd()
	}
	if exists(rendererPath) {
		w.renderer = &lineTailer{path: rendererPath}
		w.renderer.toEnd()
	}
}

func (w *LogWatcher) Poll(roots Roots) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if roots.RunLogDir != w.runDir {
		w.rebuild(roots)
		return
	}
	for _, tailer := range []*lineTailer{w.mainLog, w.renderer} {
		if tailer == nil {
			continue
		}
		lines, err := tailer.lines()
		if err != nil {
			continue
		}
		for _, line := range lines {
			if state, ok := parseControlLine(line); ok {
				w.states[state.SessionID] = state
			}
			if reconnect, ok := parseReconnectLine(line); ok {
				w.reconnecting = reconnect
			}
		}
	}
}

func (w *LogWatcher) Reconnecting() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reconnecting
}

func (w *LogWatcher) State(sessionID string) (ControlState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state, ok := w.states[sessionID]
	return state, ok
}

func (w *LogWatcher) States() map[string]ControlState {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]ControlState, len(w.states))
	for id, state := range w.states {
		out[id] = state
	}
	return out
}
