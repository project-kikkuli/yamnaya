package harness

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

func codexHome() (string, error) {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func codexStateDB() (string, error) {
	home, err := codexHome()
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	if err != nil || len(matches) == 0 {
		return "", err
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}

// CodexSessionID finds the user thread Codex recorded for a window launched in cwd at or after since, skipping claimed ids.
// A thread whose first message equals prompt wins over an earlier one. It returns "" while Codex has not written the thread yet.
func CodexSessionID(cwd string, since time.Time, prompt string, claimed map[string]bool) (string, error) {
	path, err := codexStateDB()
	if err != nil || path == "" {
		return "", err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM threads WHERE cwd = ? AND created_at_ms >= ? AND coalesce(thread_source, 'user') = 'user'
		ORDER BY (first_user_message = ?) DESC, created_at_ms ASC`, cwd, since.UnixMilli(), prompt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		if !claimed[id] {
			return id, nil
		}
	}
	return "", rows.Err()
}
