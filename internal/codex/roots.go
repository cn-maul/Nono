package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Roots struct {
	Dir       string
	StateDB   string
	HistoryDB string
}

func Locate() Roots {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".codex")
	}
	return Roots{
		Dir:       dir,
		StateDB:   newestDB(dir, "state_*.sqlite"),
		HistoryDB: newestDB(dir, "thread_history_*.sqlite"),
	}
}

// newestDB picks the highest numbered database, since Codex bumps the suffix
// across schema migrations.
func newestDB(dir, pattern string) string {
	files, _ := filepath.Glob(filepath.Join(dir, pattern))
	best := ""
	bestVersion := -1
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".sqlite")
		if i := strings.LastIndexByte(name, '_'); i >= 0 {
			name = name[i+1:]
		}
		version, err := strconv.Atoi(name)
		if err != nil {
			if best == "" {
				best = path
			}
			continue
		}
		if version > bestVersion {
			bestVersion = version
			best = path
		}
	}
	return best
}
