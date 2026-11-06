package qoder

import (
	"errors"

	"nono/internal/winfocus"
)

// DebugWindows prints the top-level Qoder windows. It backs `nono windows`.
func DebugWindows() {
	winfocus.Debug(qoderPIDs(), "qoder")
}

// FocusMainWindow restores the largest visible Qoder window and brings it to
// the foreground.
func FocusMainWindow() error {
	pids := qoderPIDs()
	if len(pids) == 0 {
		return errors.New("Qoder 未运行")
	}
	return winfocus.Focus(pids, []string{"qoder"})
}
