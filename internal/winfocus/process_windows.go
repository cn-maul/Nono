package winfocus

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PIDs returns the processes whose executable name matches.
func PIDs(match func(name string) bool) map[uint32]bool {
	pids := map[uint32]bool{}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return pids
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if match(name) {
			pids[entry.ProcessID] = true
		}
	}
	return pids
}
