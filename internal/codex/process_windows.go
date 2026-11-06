package codex

import (
	"errors"
	"strings"

	"nono/internal/winfocus"
)

func codexPIDs() map[uint32]bool {
	return winfocus.PIDs(func(name string) bool {
		return strings.Contains(name, "codex")
	})
}

func CodexRunning() bool {
	return len(codexPIDs()) > 0
}

// FocusMainWindow restores the largest Codex window and brings it to the
// foreground. A window is matched by owning process, or by title.
func FocusMainWindow() error {
	pids := codexPIDs()
	if len(pids) == 0 {
		return errors.New("Codex 未运行")
	}
	return winfocus.Focus(pids, []string{"codex"})
}

// DebugWindows prints the top-level Codex windows. It backs `nono codex-win`.
func DebugWindows() {
	winfocus.Debug(codexPIDs(), "codex")
}
