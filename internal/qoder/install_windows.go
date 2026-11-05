package qoder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type InstallInfo struct {
	Installed bool
	Path      string
	Version   string
}

// Install finds the Qoder installation, preferring the executable of the
// running process and falling back to the versioned install directories.
func Install() InstallInfo {
	if path := runningExePath(); path != "" {
		return InstallInfo{Installed: true, Path: path, Version: versionFrom(path, statusVersion())}
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local, _ = os.UserCacheDir()
	}
	roots := []string{
		filepath.Join(local, "Programs", "Qoder CN", ".qoder-versions"),
		filepath.Join(local, "Programs", "Qoder", ".qoder-versions"),
	}
	type candidate struct {
		path    string
		version string
		mod     time.Time
	}
	var found []candidate
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			for _, name := range []string{"Qoder CN.exe", "Qoder.exe"} {
				path := filepath.Join(dir, name)
				if !exists(path) {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				found = append(found, candidate{path: path, version: entry.Name(), mod: info.ModTime()})
				break
			}
		}
	}
	if len(found) == 0 {
		for _, dir := range []string{
			filepath.Join(local, "Programs", "Qoder CN"),
			filepath.Join(local, "Programs", "Qoder"),
		} {
			for _, name := range []string{"Qoder CN.exe", "Qoder.exe"} {
				if path := filepath.Join(dir, name); exists(path) {
					return InstallInfo{Installed: true, Path: path, Version: statusVersion()}
				}
			}
		}
		return InstallInfo{}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	version := found[0].version
	if !looksLikeVersion(version) {
		version = statusVersion()
	}
	return InstallInfo{Installed: true, Path: found[0].path, Version: version}
}

func runningExePath() string {
	for pid := range qoderPIDs() {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}
		buf := make([]uint16, 1024)
		size := uint32(len(buf))
		err = windows.QueryFullProcessImageName(handle, 0, &buf[0], &size)
		windows.CloseHandle(handle)
		if err == nil && size > 0 {
			return windows.UTF16ToString(buf[:size])
		}
	}
	return ""
}

func statusVersion() string {
	home, _ := os.UserHomeDir()
	for _, name := range []string{".qoder-cn/.qoder-app-status.json", ".qoder/.qoder-app-status.json"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			continue
		}
		var status struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &status) == nil && status.Version != "" {
			return status.Version
		}
	}
	return ""
}

func versionFrom(path, fallback string) string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '\\' || r == '/' })
	for i := len(parts) - 1; i >= 0; i-- {
		if looksLikeVersion(parts[i]) {
			return parts[i]
		}
	}
	return fallback
}

func looksLikeVersion(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
