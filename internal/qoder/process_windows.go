package qoder

import "nono/internal/winfocus"

func qoderPIDs() map[uint32]bool {
	return winfocus.PIDs(func(name string) bool {
		switch name {
		case "qoder cn.exe", "qoder.exe", "qoder-cn.exe":
			return true
		}
		return false
	})
}

func QoderRunning() bool {
	pids := qoderPIDs()
	if len(pids) == 0 {
		return false
	}
	// Qoder leaves helper processes behind after its window closes; require
	// the app window so the notch can hand over to another agent.
	return winfocus.HasTitle(pids, "qoder")
}
