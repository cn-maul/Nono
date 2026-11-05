package qoder

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Roots struct {
	RoamingDir  string
	UserDir     string
	DBPath      string
	BufferPath  string
	RunLogDir   string
	SessionRoot string
}

func Locate() Roots {
	roaming, _ := os.UserConfigDir()
	var r Roots
	for _, name := range []string{"com.qodercn.app.stable", "Qoder"} {
		dir := filepath.Join(roaming, name)
		if isDir(dir) {
			r.RoamingDir = dir
			break
		}
	}
	home, _ := os.UserHomeDir()
	for _, name := range []string{".qoder-cn", ".qoder"} {
		dir := filepath.Join(home, name)
		if isDir(dir) {
			r.UserDir = dir
			break
		}
	}
	if r.RoamingDir != "" {
		r.DBPath = filepath.Join(r.RoamingDir, "main.sqlite")
		r.BufferPath = filepath.Join(r.RoamingDir, "chat-session-turn-payload-buffer.sqlite")
		r.RunLogDir = latestRunDir(filepath.Join(r.RoamingDir, "logs"))
	}
	if r.UserDir != "" {
		r.SessionRoot = filepath.Join(r.UserDir, "logs", "sessions")
	}
	return r
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func latestRunDir(logs string) string {
	entries, err := os.ReadDir(logs)
	if err != nil {
		return ""
	}
	type candidate struct {
		name string
		mod  time.Time
	}
	var found []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(logs, entry.Name())
		if !exists(filepath.Join(dir, "main.log")) && !exists(filepath.Join(dir, "renderer.log")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		found = append(found, candidate{name: entry.Name(), mod: info.ModTime()})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	if len(found) == 0 {
		return ""
	}
	return filepath.Join(logs, found[0].name)
}
