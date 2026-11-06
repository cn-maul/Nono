package hub

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"nono/internal/winfocus"
)

// OpenReadOnly opens another app's SQLite database without ever writing it.
func OpenReadOnly(path string) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func PIDsContaining(needle string) map[uint32]bool {
	needle = strings.ToLower(needle)
	return winfocus.PIDs(func(name string) bool {
		return strings.Contains(name, needle)
	})
}

func RunningContaining(needle string) bool {
	return len(PIDsContaining(needle)) > 0
}

func FocusContaining(needle string, hints ...string) error {
	pids := PIDsContaining(needle)
	if len(pids) == 0 {
		return ErrNotRunning
	}
	return winfocus.Focus(pids, hints)
}

type notRunningError struct{}

func (notRunningError) Error() string { return "应用未运行" }

var ErrNotRunning error = notRunningError{}
